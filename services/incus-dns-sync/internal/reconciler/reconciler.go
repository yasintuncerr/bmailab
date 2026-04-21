package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	incusclient "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"

	"github.com/bmu-ailab/incus-dns-sync/internal/config"
	"github.com/bmu-ailab/incus-dns-sync/internal/dns"
)

// Reconciler, syncs Incus instances' DNS records to Technitium DNS.
//
// Two layers:
//   - Event watcher: takes an action like instance-started / instance-deleted
//   - Reconcile loop: each cfg.Reconciler.Interval makes a full diff and fixes inconsistencies
//
// "desired" state: dns-enabled profile and Running instances
// "actual"  state: A records in Technitium zone (only instance name format)
type Reconciler struct {
	cfg    *config.Config
	incus  incusclient.InstanceServer
	dns    *dns.Client
	logger *slog.Logger

	// reconcileCh: triggers reconcile
	reconcileCh chan struct{}

	// mu, manages managedNames
	mu sync.RWMutex
	// managedNames: instance names of DNS records managed by this service
	// Used to avoid touching DNS records created by other services
	managedNames map[string]struct{}
}

// New, creates a new Reconciler.
func New(cfg *config.Config, incusConn incusclient.InstanceServer, dnsClient *dns.Client, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		cfg:          cfg,
		incus:        incusConn,
		dns:          dnsClient,
		logger:       logger,
		reconcileCh:  make(chan struct{}, 1),
		managedNames: make(map[string]struct{}),
	}
}

// Run, starts the reconciler and runs until ctx is cancelled.
func (r *Reconciler) Run(ctx context.Context) error {
	r.logger.Info("reconciler starting",
		"zone", r.cfg.DNS.Zone,
		"profile", r.cfg.Incus.DNSProfile,
		"interval", r.cfg.Reconciler.Interval,
	)

	// Make an initial reconcile
	r.triggerReconcile()

	// Periodic reconcile ticker
	ticker := time.NewTicker(r.cfg.Reconciler.Interval)
	defer ticker.Stop()

	// Start event listener in a separate goroutine
	eventErrCh := make(chan error, 1)
	go func() {
		eventErrCh <- r.watchEvents(ctx)
	}()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reconciler durduruluyor")
			return nil

		case err := <-eventErrCh:
			if ctx.Err() != nil {
				return nil // normal shutdown
			}
			// Event stream kesildi; reconcile loop çalışmaya devam eder,
			// event watcher yeniden başlatılır.
			r.logger.Error("event stream kesildi, yeniden başlatılıyor", "err", err)
			go func() {
				time.Sleep(5 * time.Second)
				eventErrCh <- r.watchEvents(ctx)
			}()

		case <-ticker.C:
			r.logger.Debug("periyodik reconcile tetiklendi")
			r.triggerReconcile()

		case <-r.reconcileCh:
			if err := r.reconcile(ctx); err != nil {
				r.logger.Error("reconcile başarısız", "err", err)
			}
		}
	}
}

// triggerReconcile, reconcileCh'ya non-blocking olarak sinyal gönderir.
// Kanalda zaten sinyal varsa yeni sinyal eklenmez (coalescence).
func (r *Reconciler) triggerReconcile() {
	select {
	case r.reconcileCh <- struct{}{}:
	default:
	}
}

// watchEvents, Incus lifecycle event'lerini dinler ve reconcile tetikler.
func (r *Reconciler) watchEvents(ctx context.Context) error {
	listener, err := r.incus.GetEventsAllProjects()
	if err != nil {
		return fmt.Errorf("event listener oluşturulamadı: %w", err)
	}
	defer listener.Disconnect()

	r.logger.Info("lifecycle event dinleyici başlatıldı")

	handler := func(event api.Event) {
		if event.Type != "lifecycle" {
			return
		}

		var meta api.EventLifecycle
		if err := json.Unmarshal(event.Metadata, &meta); err != nil {
			r.logger.Warn("event parse hatası", "err", err)
			return
		}

		// Sadece instance event'leri
		if !strings.HasPrefix(meta.Action, "instance-") {
			return
		}

		// Instance adını source'dan çıkar: /1.0/instances/ali-ws01 → ali-ws01
		instanceName := sourceToName(meta.Source)
		if instanceName == "" {
			return
		}

		r.logger.Debug("lifecycle event",
			"action", meta.Action,
			"instance", instanceName,
		)

		switch meta.Action {
		case "instance-started":
			// Başladıktan hemen sonra IP henüz atanmamış olabilir,
			// kısa bir bekleme sonrası reconcile yeterli.
			go func() {
				time.Sleep(r.cfg.Incus.IPPollInterval)
				r.triggerReconcile()
			}()

		case "instance-deleted":
			// Silme anında instance artık listede yok; reconcile diff alır ve kaydı siler.
			r.triggerReconcile()
		}
	}

	_, err = listener.AddHandler([]string{"lifecycle"}, handler)
	if err != nil {
		return fmt.Errorf("handler eklenemedi: %w", err)
	}

	// ctx iptal edildiğinde listener'ı kapat
	go func() {
		<-ctx.Done()
		listener.Disconnect()
	}()

	return listener.Wait()
}

// reconcile, tek bir tam senkronizasyon döngüsü çalıştırır.
//
//	desired = dns-enabled profiline sahip, Running instance'lar → map[name]ip
//	actual  = Technitium zone'undaki A kayıtları → map[name]ip
//
// diff:
//   - desired'da var, actual'da yok        → UpsertA
//   - desired'da var, actual'da var ama IP farklı → UpsertA
//   - desired'da yok, actual'da var, managed → DeleteA
func (r *Reconciler) reconcile(ctx context.Context) error {
	r.logger.Debug("reconcile başladı")

	desired, err := r.desiredState(ctx)
	if err != nil {
		return fmt.Errorf("desired state alınamadı: %w", err)
	}

	actual, err := r.dns.ZoneRecords(ctx)
	if err != nil {
		return fmt.Errorf("actual state alınamadı: %w", err)
	}

	// managedNames'i güncelle: desired + önceden yönetilenler
	r.mu.Lock()
	for name := range desired {
		r.managedNames[name] = struct{}{}
	}
	r.mu.Unlock()

	r.mu.RLock()
	managed := make(map[string]struct{}, len(r.managedNames))
	for k, v := range r.managedNames {
		managed[k] = v
	}
	r.mu.RUnlock()

	adds, updates, deletes := 0, 0, 0

	// Ekle / güncelle
	for name, desiredIP := range desired {
		fqdn := dns.FQDN(name, r.cfg.DNS.Zone)
		actualIP, exists := actual[name]

		if !exists {
			r.logger.Info("DNS ekleniyor", "fqdn", fqdn, "ip", desiredIP)
			if err := r.dns.UpsertA(ctx, fqdn, desiredIP); err != nil {
				r.logger.Error("DNS eklenemedi", "fqdn", fqdn, "err", err)
				continue
			}
			adds++
		} else if actualIP != desiredIP {
			r.logger.Info("DNS güncelleniyor", "fqdn", fqdn, "old_ip", actualIP, "new_ip", desiredIP)
			if err := r.dns.UpsertA(ctx, fqdn, desiredIP); err != nil {
				r.logger.Error("DNS güncellenemedi", "fqdn", fqdn, "err", err)
				continue
			}
			updates++
		}
	}

	// Sil — sadece bu servis tarafından yönetilen ve artık desired'da olmayan kayıtlar
	for name, actualIP := range actual {
		if _, inDesired := desired[name]; inDesired {
			continue
		}
		if _, isManaged := managed[name]; !isManaged {
			// Bu servis bu kaydı oluşturmadı, dokunma
			continue
		}

		fqdn := dns.FQDN(name, r.cfg.DNS.Zone)
		r.logger.Info("DNS siliniyor", "fqdn", fqdn, "ip", actualIP)
		if err := r.dns.DeleteA(ctx, fqdn, actualIP); err != nil {
			r.logger.Error("DNS silinemedi", "fqdn", fqdn, "err", err)
			continue
		}

		// managedNames'den çıkar
		r.mu.Lock()
		delete(r.managedNames, name)
		r.mu.Unlock()

		deletes++
	}

	if adds+updates+deletes > 0 {
		r.logger.Info("reconcile tamamlandı",
			"added", adds,
			"updated", updates,
			"deleted", deletes,
		)
	} else {
		r.logger.Debug("reconcile tamamlandı — değişiklik yok")
	}

	return nil
}

// desiredState, dns-enabled profiline sahip Running instance'ları ve IP'lerini döner.
func (r *Reconciler) desiredState(ctx context.Context) (map[string]string, error) {
	instances, err := r.incus.GetInstancesFull(api.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("instance listesi alınamadı: %w", err)
	}

	desired := make(map[string]string)

	for _, inst := range instances {
		// Sadece dns-enabled profili olanlar
		if !hasProfile(inst.Profiles, r.cfg.Incus.DNSProfile) {
			continue
		}
		// Sadece Running olanlar
		if inst.State == nil || inst.State.Status != "Running" {
			continue
		}

		ip, err := r.extractIP(inst.State)
		if err != nil {
			r.logger.Warn("IP alınamadı",
				"instance", inst.Name,
				"err", err,
			)
			// IP bekleme döngüsü: instance yeni başlamış olabilir
			go r.waitAndTrigger(inst.Name)
			continue
		}

		desired[inst.Name] = ip
	}

	return desired, nil
}

// waitAndTrigger, instance'ın IP almasını bekler ve reconcile tetikler.
// Bu sayede event watcher ile reconcile loop arasındaki race condition kapatılır.
func (r *Reconciler) waitAndTrigger(instanceName string) {
	deadline := time.Now().Add(r.cfg.Incus.IPWaitTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(r.cfg.Incus.IPPollInterval)

		inst, _, err := r.incus.GetInstanceFull(instanceName)
		if err != nil {
			return // instance silinmiş olabilir
		}
		if inst.State == nil || inst.State.Status != "Running" {
			return
		}

		if _, err := r.extractIP(inst.State); err == nil {
			r.logger.Debug("IP alındı, reconcile tetikleniyor", "instance", instanceName)
			r.triggerReconcile()
			return
		}
	}
	r.logger.Error("IP bekleme timeout",
		"instance", instanceName,
		"timeout", r.cfg.Incus.IPWaitTimeout,
	)
}

// extractIP, instance state'inden yapılandırılan arayüzün IPv4 adresini çıkarır.
func (r *Reconciler) extractIP(state *api.InstanceState) (string, error) {
	iface, ok := state.Network[r.cfg.Incus.Interface]
	if !ok {
		return "", fmt.Errorf("arayüz bulunamadı: %s", r.cfg.Incus.Interface)
	}

	for _, addr := range iface.Addresses {
		if addr.Family == "inet" && !strings.HasPrefix(addr.Address, "127.") {
			return addr.Address, nil
		}
	}
	return "", fmt.Errorf("IPv4 adresi yok (%s)", r.cfg.Incus.Interface)
}

// hasProfile, verilen profil listesinde hedef profilin olup olmadığını kontrol eder.
func hasProfile(profiles []string, target string) bool {
	for _, p := range profiles {
		if p == target {
			return true
		}
	}
	return false
}

// sourceToName, Incus event source string'inden instance adını çıkarır.
// "/1.0/instances/ali-ws01"  →  "ali-ws01"
// "/1.0/instances/ali-ws01?project=default"  →  "ali-ws01"
func sourceToName(source string) string {
	// Query string'i at
	if i := strings.Index(source, "?"); i != -1 {
		source = source[:i]
	}
	parts := strings.Split(strings.TrimPrefix(source, "/"), "/")
	// ["1.0", "instances", "ali-ws01"]
	if len(parts) >= 3 && parts[1] == "instances" {
		return parts[2]
	}
	return ""
}
