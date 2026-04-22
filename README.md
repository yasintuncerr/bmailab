# BMU AI Lab

Bu repository, Uludağ Üniversitesi Bilgisayar Mühendisliği Bölümü bünyesinde kurulan Yapay Zekâ Laboratuvarı'nın resmi kaynaklarını içermektedir.

BMU AI Lab; yapay zekâ alanında araştırma, geliştirme ve eğitim faaliyetlerini desteklemek amacıyla kurulmuştur. Laboratuvar altyapısı, yüksek performanslı hesaplama kaynaklarının yanı sıra modern container ve ağ yönetim teknolojileri ile desteklenmektedir.

---

## 🧩 Altyapı Mimarisi

Laboratuvar altyapısı, **Incus tabanlı container orchestration** yaklaşımı ile yönetilmektedir.

- Tüm compute kaynakları **Incus cluster yapısı** altında toplanır
- Kullanıcı ortamları izole **container instance**’lar olarak oluşturulur
- Her instance, standart bir **default profile** ile otomatik yapılandırılır
- Instance’lar oluşturulduğunda olay (event) bazlı otomasyonlar tetiklenir

### 🔹 Ağ ve Trafik Akışı

- Ana host (`core-infra`), dış ağdan aldığı trafiği iç ağa dağıtır
- İki temel ağ segmenti bulunur:

| Ağ                         | Amaç                             |
| -------------------------- | -------------------------------- |
| `incusbr0 (10.14.21.0/24)` | Incus container instance’ları    |
| `enp6s0f1 (10.10.0.0/24)`  | Cluster node’ları arası iletişim |

- Tüm container’lar NAT üzerinden dış ağa çıkar
- İç ağ gateway: `infra-gateway` container’ı

---

### 🔹 Servis Katmanları

Altyapı, katmanlı bir servis mimarisi ile çalışır:

| Katman    | Teknoloji      | Açıklama                         |
| --------- | -------------- | -------------------------------- |
| DNS       | Technitium DNS | İç DNS yönetimi ve dinamik kayıt |
| Proxy     | Traefik v3     | HTTP ve SSH giriş noktası        |
| Auth      | Authelia       | Kimlik doğrulama (SSO)           |
| Container | Incus          | Instance yönetimi                |

---

### 🔹 Dinamik DNS Yapısı

- Tüm iç DNS yönetimi **Technitium DNS** üzerinden yapılır
- `*.bmlab.uludag.edu.tr` wildcard kaydı Traefik’e yönlendirilir
- Instance oluşturulduğunda:

1. Incus lifecycle event tetiklenir
2. Event listener servisi instance bilgilerini alır
3. Instance IP adresi resolve edilir
4. Technitium API üzerinden otomatik DNS kaydı açılır

📌 Örnek:

```
ali-ws01 → ali-ws01.bmlab.uludag.edu.tr → 10.14.21.x
```

Bu sayede kullanıcılar instance’larına doğrudan domain üzerinden erişebilir:

```bash
ssh user@ali-ws01.bmlab.uludag.edu.tr
```

---

### 🔹 Yönetim Yaklaşımı

- Cloud-init tabanlı dağıtımlar minimize edilmiştir
- DNS yönetimi container içinden değil, merkezi servis üzerinden yapılır
- Event-driven otomasyon ile:
  - Manuel DNS ihtiyacı ortadan kaldırılır
  - Distro bağımlılığı kaldırılır (Ubuntu, Alpine vs.)

- Tüm sistem **stateless + yeniden üretilebilir** olacak şekilde tasarlanmıştır

---

## 🖥️ Donanım Altyapısı

### 🔹 Core Infrastructure Node

| Bileşen           | Detay                             |
| ----------------- | --------------------------------- |
| CPU               | Intel Xeon E5-1650 v2 @ 3.50GHz   |
| Çekirdek / Thread | 6 Core / 12 Thread                |
| RAM               | 16 GB DDR3 ECC (4x4GB, 1866 MT/s) |
| Maks. RAM         | 128 GB                            |
| Disk              | 1 TB HDD                          |
| Ek Disk           | 2 TB HDD                          |

---

### 🔹 Compute Node

| Bileşen           | Detay                               |
| ----------------- | ----------------------------------- |
| CPU               | AMD Ryzen Threadripper PRO 9955WX   |
| Çekirdek / Thread | 16 Core / 32 Thread                 |
| RAM               | 128 GB ECC DDR5 (8x16GB, 5600 MT/s) |
| Maks. RAM         | 4 TB                                |
| GPU               | 2x NVIDIA GeForce RTX 5090          |
| GPU Bellek        | 64 GB                               |
| NVMe              | 2 TB                                |
| HDD               | 2x 8 TB                             |

---

## ⚙️ Genel Özellikler

- Incus cluster tabanlı ölçeklenebilir yapı
- Event-driven otomasyon altyapısı
- Merkezi DNS ve erişim yönetimi
- GPU destekli yüksek performanslı hesaplama
- NVMe + yüksek kapasiteli disk mimarisi

---

## 📚 Dokümantasyon

| # | Doküman | Açıklama |
|---|---------|----------|
| 01 | [Ağ & Yönlendirme](docs/01.Network&Routing.md) | İç ağ yapısı, NAT, VLAN ve yönlendirme kuralları |
| 02 | [Incus Kurulumu](docs/02.Incus-installiation.md) | Incus cluster kurulumu, profil tanımları ve instance yönetimi |
