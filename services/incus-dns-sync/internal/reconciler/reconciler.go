package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
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
			r.logger.Info("reconciler stopping")
			return nil

		case err := <-eventErrCh:
			if ctx.Err() != nil {
				return nil // normal shutdown
			}
			// Event stream disconnected; reconcile loop continues,
			// event watcher gets restarted.
			r.logger.Error("event stream disconnected, restarting", "err", err)
			go func() {
				time.Sleep(5 * time.Second)
				eventErrCh <- r.watchEvents(ctx)
			}()

		case <-ticker.C:
			r.logger.Debug("periodic reconcile triggered")
			r.triggerReconcile()

		case <-r.reconcileCh:
			if err := r.reconcile(ctx); err != nil {
				r.logger.Error("reconcile failed", "err", err)
			}
		}
	}
}

// triggerReconcile sends a non-blocking signal to reconcileCh.
// If there is already a signal in the channel, a new one is not added (coalescence).
func (r *Reconciler) triggerReconcile() {
	select {
	case r.reconcileCh <- struct{}{}:
	default:
	}
}

// watchEvents listens to Incus lifecycle events and triggers reconcile.
func (r *Reconciler) watchEvents(ctx context.Context) error {
	listener, err := r.incus.GetEventsAllProjects()
	if err != nil {
		return fmt.Errorf("failed to create event listener: %w", err)
	}
	defer listener.Disconnect()

	r.logger.Info("lifecycle event listener started")

	handler := func(event api.Event) {
		if event.Type != "lifecycle" {
			return
		}

		var meta api.EventLifecycle
		if err := json.Unmarshal(event.Metadata, &meta); err != nil {
			r.logger.Warn("event parse error", "err", err)
			return
		}

		// Only instance events
		if !strings.HasPrefix(meta.Action, "instance-") {
			return
		}

		// Extract instance name and project from source: /1.0/instances/ali-ws01?project=system
		projectName, instanceName := sourceToProjectAndName(meta.Source)
		if instanceName == "" {
			return
		}

		r.logger.Info("lifecycle event received",
			"action", meta.Action,
			"instance", instanceName,
			"project", projectName,
		)

		switch meta.Action {
		case "instance-started":
			// Right after starting, IP might not be assigned yet,
			// a short wait before reconcile is sufficient.
			go func() {
				time.Sleep(r.cfg.Incus.IPPollInterval)
				r.triggerReconcile()
			}()

		case "instance-deleted":
			// On deletion, instance is no longer in the list; reconcile takes diff and deletes record.
			r.triggerReconcile()
		}
	}

	_, err = listener.AddHandler([]string{"lifecycle"}, handler)
	if err != nil {
		return fmt.Errorf("failed to add handler: %w", err)
	}

	// close listener when ctx is cancelled
	go func() {
		<-ctx.Done()
		listener.Disconnect()
	}()

	return listener.Wait()
}

// reconcile runs a single full synchronization loop.
//
//	desired = Running instances with dns-enabled profile → map[name]ip
//	actual  = A records in Technitium zone → map[name]ip
//
// diff:
//   - in desired, not in actual        → UpsertA
//   - in desired, in actual but different IP → UpsertA
//   - not in desired, in actual, managed → DeleteA
func (r *Reconciler) reconcile(ctx context.Context) error {
	r.logger.Debug("reconcile started")

	desired, err := r.desiredState(ctx)
	if err != nil {
		return fmt.Errorf("failed to get desired state: %w", err)
	}

	actual, err := r.dns.ZoneRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get actual state: %w", err)
	}

	// update managedNames: desired + previously managed
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

	r.logger.Info("reconcile dataset", "desired", len(desired), "actual", len(actual), "managed", len(managed))

	adds, updates, deletes := 0, 0, 0

	// Add / update
	for name, desiredIP := range desired {
		fqdn := dns.FQDN(name, r.cfg.DNS.Zone)
		actualIP, exists := actual[name]

		if !exists {
			r.logger.Info("DNS adding", "fqdn", fqdn, "ip", desiredIP)
			if err := r.dns.UpsertA(ctx, fqdn, desiredIP); err != nil {
				r.logger.Error("failed to add DNS", "fqdn", fqdn, "err", err)
				continue
			}
			adds++
		} else if actualIP != desiredIP {
			r.logger.Info("DNS updating", "fqdn", fqdn, "old_ip", actualIP, "new_ip", desiredIP)
			if err := r.dns.UpsertA(ctx, fqdn, desiredIP); err != nil {
				r.logger.Error("failed to update DNS", "fqdn", fqdn, "err", err)
				continue
			}
			updates++
		}
	}

	// Delete — only records managed by this service that are no longer in desired
	for name, actualIP := range actual {
		if _, inDesired := desired[name]; inDesired {
			continue
		}
		if _, isManaged := managed[name]; !isManaged {
			// This service didn't create this record, do not touch
			continue
		}

		fqdn := dns.FQDN(name, r.cfg.DNS.Zone)
		r.logger.Info("DNS deleting", "fqdn", fqdn, "ip", actualIP)
		if err := r.dns.DeleteA(ctx, fqdn, actualIP); err != nil {
			r.logger.Error("failed to delete DNS", "fqdn", fqdn, "err", err)
			continue
		}

		// remove from managedNames
		r.mu.Lock()
		delete(r.managedNames, name)
		r.mu.Unlock()

		deletes++
	}

	if adds+updates+deletes > 0 {
		r.logger.Info("reconcile completed",
			"added", adds,
			"updated", updates,
			"deleted", deletes,
		)
	} else {
		r.logger.Info("reconcile completed — no changes")
	}

	return nil
}

// desiredState returns Running instances and their IPs with dns-enabled profile across all projects.
func (r *Reconciler) desiredState(ctx context.Context) (map[string]string, error) {
	desired := make(map[string]string)

	projects, err := r.incus.GetProjects()
	if err != nil {
		return nil, fmt.Errorf("failed to get project list: %w", err)
	}

	for _, p := range projects {
		cProject := r.incus.UseProject(p.Name)
		instances, err := cProject.GetInstancesFull(api.InstanceTypeAny)
		if err != nil {
			r.logger.Warn("failed to get project instance list", "project", p.Name, "err", err)
			continue
		}

		for _, inst := range instances {
			// Only those with dns-enabled profile
			if !hasProfile(inst.Profiles, r.cfg.Incus.DNSProfile) {
				continue
			}
			// Only Running ones
			if inst.State == nil || inst.State.Status != "Running" {
				continue
			}

			ip, err := r.extractIP(inst.State)
			if err != nil {
				r.logger.Info("IP not yet retrieved, waiting",
					"instance", inst.Name,
					"project", p.Name,
					"err", err,
				)
				// IP wait loop: instance might have just started
				go r.waitAndTrigger(p.Name, inst.Name)
				continue
			}

			desired[inst.Name] = ip
		}
	}

	return desired, nil
}

// waitAndTrigger waits for the instance to get an IP and triggers reconcile.
// This resolves the race condition between event watcher and reconcile loop.
func (r *Reconciler) waitAndTrigger(projectName, instanceName string) {
	deadline := time.Now().Add(r.cfg.Incus.IPWaitTimeout)
	cProject := r.incus.UseProject(projectName)

	for time.Now().Before(deadline) {
		time.Sleep(r.cfg.Incus.IPPollInterval)

		inst, _, err := cProject.GetInstanceFull(instanceName)
		if err != nil {
			return // instance might be deleted
		}
		if inst.State == nil || inst.State.Status != "Running" {
			return
		}

		if _, err := r.extractIP(inst.State); err == nil {
			r.logger.Info("IP retrieved, triggering reconcile", "instance", instanceName)
			r.triggerReconcile()
			return
		}
	}
	r.logger.Error("IP wait timeout",
		"project", projectName,
		"instance", instanceName,
		"timeout", r.cfg.Incus.IPWaitTimeout,
	)
}

// extractIP extracts the IPv4 address of the configured interface from the instance state.
func (r *Reconciler) extractIP(state *api.InstanceState) (string, error) {
	iface, ok := state.Network[r.cfg.Incus.Interface]
	if !ok {
		return "", fmt.Errorf("interface not found: %s", r.cfg.Incus.Interface)
	}

	for _, addr := range iface.Addresses {
		if addr.Family == "inet" && !strings.HasPrefix(addr.Address, "127.") {
			return addr.Address, nil
		}
	}
	return "", fmt.Errorf("no IPv4 address (%s)", r.cfg.Incus.Interface)
}

// hasProfile checks if the target profile exists in the given profile list.
func hasProfile(profiles []string, target string) bool {
	for _, p := range profiles {
		if p == target {
			return true
		}
	}
	return false
}

// sourceToProjectAndName extracts project and instance name from Incus event source string.
// "/1.0/instances/ali-ws01"  →  "default", "ali-ws01"
// "/1.0/instances/ali-ws01?project=system"  →  "system", "ali-ws01"
func sourceToProjectAndName(source string) (string, string) {
	proj := "default"
	if i := strings.Index(source, "?"); i != -1 {
		q := source[i+1:]
		source = source[:i]
		if vals, err := url.ParseQuery(q); err == nil {
			if p := vals.Get("project"); p != "" {
				proj = p
			}
		}
	}
	parts := strings.Split(strings.TrimPrefix(source, "/"), "/")
	if len(parts) >= 3 && parts[1] == "instances" {
		return proj, parts[2]
	}
	return proj, ""
}
