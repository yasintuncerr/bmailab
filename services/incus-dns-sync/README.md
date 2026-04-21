# incus-dns-sync

Incus instance lifecycle olaylarını dinleyerek Technitium DNS'e otomatik A kaydı açan / kapatan Go servisi.

## Nasıl Çalışır?

İki katmanlı bir mimari kullanır:

| Katman | Açıklama |
|---|---|
| **Event Watcher** | Incus Unix socket'inden lifecycle event'leri dinler (`instance-started`, `instance-deleted`). Event geldiğinde reconcile tetikler. |
| **Reconcile Loop** | Her `reconciler.interval`'de (varsayılan 5 dk) tam bir diff alır ve tutarsızlıkları düzeltir. Servis yeniden başlatıldığında veya event kaçırıldığında garantör görevi görür. |

**Desired state:** `dns-enabled` profiline sahip + `Running` durumdaki instance'lar  
**Actual state:** Technitium zone'undaki A kayıtları  

Diff sonucu:
- Desired'da var, actual'da yok → `UpsertA`
- Desired'da var, actual'da var ama IP farklı → `UpsertA`
- Desired'da yok, actual'da var, bu servis tarafından yönetiliyor → `DeleteA`

## Kurulum

### Gereksinimler

- Çalışan bir Incus sunucusu
- Çalışan bir Technitium DNS sunucusu (API erişimi ile)
- Hedef sistemde Go 1.22+ (build için, çalıştırmak için gerekli değil)

### Build & Install

```bash
# Derle (linux/amd64)
make build

# /usr/local/bin'e kur, config dosyasını kopyala, systemd unit'i etkinleştir
sudo make install
```

Cross-compile örneği (Mac'te build edip Linux'a gönder):
```bash
make build GOOS=linux GOARCH=arm64
```

### Sonraki Adımlar

```bash
# 1. Config dosyasını düzenle
nano /etc/incus-dns-sync/incus-dns-sync.yaml

# 2. DNS-enabled profile oluştur
incus profile create dns-enabled

# 3. Servisi başlat
systemctl start incus-dns-sync

# 4. Logları izle
journalctl -fu incus-dns-sync
```

## Yapılandırma

Config dosyası: `/etc/incus-dns-sync/incus-dns-sync.yaml`

```yaml
technitium:
  api_base: "http://<TECHNITIUM_HOST>:<PORT>"   # trailing slash olmadan
  token: "<TECHNITIUM_API_TOKEN>"               # Administration → Sessions
  timeout: 15s

incus:
  socket_path: ""          # boş = varsayılan /var/lib/incus/unix.socket
  dns_profile: "dns-enabled"
  interface: "eth0"
  ip_wait_timeout: 60s
  ip_poll_interval: 3s

dns:
  zone: "lab.example.com"
  ttl: 300

reconciler:
  interval: 5m

log:
  level: "info"   # debug | info | warn | error
```

## Proje Yapısı

```
incus-dns-sync/
├── cmd/incus-dns-sync/
│   └── main.go               # Giriş noktası
├── internal/
│   ├── config/
│   │   └── config.go         # YAML config yükleme + validasyon
│   ├── dns/
│   │   └── technitium.go     # Technitium DNS API istemcisi
│   └── reconciler/
│       └── reconciler.go     # Core iş mantığı
├── incus-dns-sync.yaml       # Örnek config
├── incus-dns-sync.service    # systemd unit dosyası
├── Makefile
└── go.mod
```

## Makefile Hedefleri

| Hedef | Açıklama |
|---|---|
| `make build` | Binary derler → `build/incus-dns-sync` |
| `make install` | Derler + `/usr/local/bin`'e kurar + systemd'yi yapılandırır |
| `make clean` | `build/` dizinini siler |
