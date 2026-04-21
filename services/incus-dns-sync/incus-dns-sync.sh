#!/usr/bin/env bash
# =============================================================================
# incus-dns-sync — Incus lifecycle event dinleyici → Technitium DNS kaydı
#
# Her yeni Incus instance'ı başladığında otomatik olarak
#   <instance-adı>.<DNS_ZONE>  →  <instance-IP>
# şeklinde A kaydı açar; instance silindiğinde kaydı kaldırır.
#
# Bağımlılıklar: bash, curl, python3 (stdlib), incus
# =============================================================================
 
set -euo pipefail

# =============================================================================
# Config yükleme
# =============================================================================
 
CONFIG_FILE="${INCUS_DNS_SYNC_CONFIG:-/etc/incus-dns-sync/incus-dns-sync.conf}"
 
if [[ ! -f "$CONFIG_FILE" ]]; then
    echo "[FATAL] Config dosyası bulunamadı: $CONFIG_FILE" >&2
    exit 1
fi
 
# shellcheck source=/dev/null
source "$CONFIG_FILE"


# =============================================================================
# Loglama
# =============================================================================

declare -A LOG_LEVELS=([DEBUG]=0 [INFO]=1 [WARN]=2 [ERROR]=3 [FATAL]=4)
CURRENT_LOG_LEVEL="${LOG_LEVELS[${LOG_LEVEL:-INFO}]}"
 
_log_rotate() {
    [[ ! -f "$LOG_FILE" ]] && return
    local size
    size=$(stat -c%s "$LOG_FILE" 2>/dev/null || echo 0)
    if (( size >= LOG_MAX_BYTES )); then
        for i in $(seq $(( LOG_KEEP_FILES - 1 )) -1 1); do
            [[ -f "${LOG_FILE}.${i}" ]] && mv "${LOG_FILE}.${i}" "${LOG_FILE}.$(( i + 1 ))"
        done
        mv "$LOG_FILE" "${LOG_FILE}.1"
    fi
}
 
log() {
    local level="$1"; shift
    local level_num="${LOG_LEVELS[$level]:-1}"
    if (( level_num < CURRENT_LOG_LEVEL )); then return 0; fi
 
    local ts
    ts="$(date '+%Y-%m-%d %H:%M:%S')"
    local msg="[$ts] [$level] $*"
 
    echo "$msg"
 
    if [[ -n "$LOG_FILE" ]]; then
        mkdir -p "$(dirname "$LOG_FILE")"
        _log_rotate
        echo "$msg" >> "$LOG_FILE"
    fi
 
    # FATAL ise stderr'e de yaz
    if (( level_num >= LOG_LEVELS[FATAL] )); then echo "$msg" >&2; fi
}


 
# =============================================================================
# Lock mekanizması
# =============================================================================
 
_acquire_lock() {
    if [[ -f "$LOCK_FILE" ]]; then
        local old_pid
        old_pid=$(cat "$LOCK_FILE" 2>/dev/null || echo "")
        if [[ -n "$old_pid" ]] && kill -0 "$old_pid" 2>/dev/null; then
            log "FATAL" "Servis zaten çalışıyor (PID: $old_pid). Lock: $LOCK_FILE"
            exit 1
        fi
        log "WARN" "Eski lock dosyası bulundu (ölü PID: $old_pid), temizleniyor."
        rm -f "$LOCK_FILE"
    fi
    echo $$ > "$LOCK_FILE"
    log "DEBUG" "Lock alındı (PID: $$)"
}
 
_release_lock() {
    rm -f "$LOCK_FILE"
    log "DEBUG" "Lock serbest bırakıldı"
}


# =============================================================================
# Temiz kapanış (signal trap)
# =============================================================================
 
_shutdown() {
    log "INFO" "Kapatma sinyali alındı, temizleniyor..."
    _release_lock
    # Arka planda çalışan handler'ları bekle
    wait 2>/dev/null || true
    log "INFO" "incus-dns-sync durduruldu"
    exit 0
}
 
trap '_shutdown' SIGTERM SIGINT SIGHUP


# =============================================================================
# Technitium DNS API
# =============================================================================
 
# Genel API çağrısı — başarıysa 0, hata varsa 1 döner
# Kullanım: _technitium_call <endpoint> [param=değer ...]
_technitium_call() {
    local endpoint="$1"; shift
    local url="${TECHNITIUM_API}${endpoint}"
    local params="token=${TECHNITIUM_TOKEN}"
 
    for param in "$@"; do
        params+="&$(python3 -c "
import sys, urllib.parse
k, v = sys.argv[1].split('=', 1)
print(k + '=' + urllib.parse.quote(v))
" "$param" 2>/dev/null || echo "$param")"
    done
 
    local response http_code body
    response=$(curl -sf \
        --connect-timeout 5 \
        --max-time 15 \
        --write-out "\n%{http_code}" \
        "${url}?${params}" 2>&1) || {
        log "ERROR" "API ulaşılamıyor: ${url}"
        return 1
    }
 
    http_code=$(echo "$response" | tail -1)
    body=$(echo "$response" | head -n -1)
 
    if [[ "$http_code" != "200" ]]; then
        log "ERROR" "API HTTP $http_code — endpoint: $endpoint"
        log "DEBUG" "Yanıt: $body"
        return 1
    fi
 
    local status
    status=$(echo "$body" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('status', 'unknown'))
except Exception as e:
    print('parse-error: ' + str(e))
" 2>/dev/null)
 
    if [[ "$status" != "ok" ]]; then
        local error_msg
        error_msg=$(echo "$body" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    print(d.get('errorMessage', ''))
except: pass
" 2>/dev/null)
        log "ERROR" "API hatası — status: $status | mesaj: $error_msg | endpoint: $endpoint"
        return 1
    fi
 
    echo "$body"
    return 0
}

# Belirtilen FQDN için mevcut A kaydının IP'sini döner; yoksa boş string
dns_get_ip() {
    local fqdn="$1"
    local response
    response=$(_technitium_call "/api/zones/records/get" \
        "domain=${fqdn}" \
        "type=A" 2>/dev/null) || { echo ""; return 0; }
 
    echo "$response" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    records = d.get('response', {}).get('records', [])
    for r in records:
        ip = r.get('rData', {}).get('ipAddress', '')
        if ip:
            print(ip)
            break
except: pass
" 2>/dev/null || echo ""
}
 
# A kaydı ekler / günceller
dns_add() {
    local fqdn="$1"
    local ip="$2"
 
    # Zaten doğru kayıt varsa tekrar ekleme
    local current_ip
    current_ip=$(dns_get_ip "$fqdn")
    if [[ "$current_ip" == "$ip" ]]; then
        log "DEBUG" "DNS kaydı zaten güncel: $fqdn → $ip"
        return 0
    fi
 
    _technitium_call "/api/zones/records/add" \
        "domain=${fqdn}" \
        "type=A" \
        "ipAddress=${ip}" \
        "ttl=${DNS_TTL}" \
        "overwrite=true" > /dev/null || return 1
 
    log "INFO" "DNS eklendi: $fqdn → $ip"
}
 
# A kaydını siler
dns_delete() {
    local fqdn="$1"
 
    local current_ip
    current_ip=$(dns_get_ip "$fqdn")
 
    if [[ -z "$current_ip" ]]; then
        log "WARN" "Silinecek DNS kaydı bulunamadı: $fqdn"
        return 0
    fi
 
    _technitium_call "/api/zones/records/delete" \
        "domain=${fqdn}" \
        "type=A" \
        "ipAddress=${current_ip}" > /dev/null || return 1
 
    log "INFO" "DNS silindi: $fqdn (eski IP: $current_ip)"
}

# =============================================================================
# Instance IP çözümleme
# =============================================================================
 
get_instance_ip() {
    local name="$1"
    local deadline=$(( SECONDS + IP_WAIT_TIMEOUT ))
    local attempt=0
 
    while (( SECONDS < deadline )); do
        (( attempt++ ))
        local ip
        ip=$(incus list "$name" --format=json 2>/dev/null | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    if not data:
        sys.exit(1)
    iface = data[0].get('state', {}).get('network', {}).get('$INSTANCE_IFACE', {})
    for addr in iface.get('addresses', []):
        if addr.get('family') == 'inet' and not addr['address'].startswith('127.'):
            print(addr['address'])
            break
except:
    sys.exit(1)
" 2>/dev/null || echo "")
 
        if [[ -n "$ip" ]]; then
            log "DEBUG" "$name IP alındı: $ip (deneme: $attempt)"
            echo "$ip"
            return 0
        fi
 
        log "DEBUG" "$name — IP bekleniyor (deneme: $attempt, kalan: $(( deadline - SECONDS ))s)"
        sleep "$IP_POLL_INTERVAL"
    done
 
    log "ERROR" "$name — $IP_WAIT_TIMEOUT saniyede IP alınamadı"
    return 1
}

# =============================================================================
# Event handler'lar
# =============================================================================
 
handle_started() {
    local name="$1"
    local fqdn="${name}.${DNS_ZONE}"
 
    log "INFO" "instance-started: $name — IP bekleniyor"
 
    local ip
    if ! ip=$(get_instance_ip "$name"); then
        log "ERROR" "handle_started başarısız: $name"
        return 1
    fi
 
    if ! dns_add "$fqdn" "$ip"; then
        log "ERROR" "DNS eklenemedi: $fqdn → $ip"
        return 1
    fi
}
 
handle_deleted() {
    local name="$1"
    local fqdn="${name}.${DNS_ZONE}"
 
    log "INFO" "instance-deleted: $name — DNS kaydı kaldırılıyor"
 
    dns_delete "$fqdn" || log "ERROR" "DNS silinemedi: $fqdn"
}
 
# Durdurulmuş instance'lar için kayıt korunur.
# İleride instance-stopped → dns_delete davranışı istenirse buradan etkinleştir.
handle_stopped() {
    local name="$1"
    log "DEBUG" "instance-stopped: $name — DNS kaydı korunuyor"
}
 
# =============================================================================
# Mevcut instance'ları başlangıçta senkronize et
# (servis yeniden başlatıldığında eksik kayıtları tamamla)
# =============================================================================
 
sync_existing() {
    log "INFO" "Mevcut RUNNING instance'lar taranıyor..."
 
    local entries
    entries=$(incus list --format=json 2>/dev/null | python3 -c "
import sys, json
try:
    data = json.load(sys.stdin)
    for inst in data:
        if inst.get('status') != 'Running':
            continue
        name = inst['name']
        iface = inst.get('state', {}).get('network', {}).get('$INSTANCE_IFACE', {})
        for addr in iface.get('addresses', []):
            if addr.get('family') == 'inet' and not addr['address'].startswith('127.'):
                print(name + ' ' + addr['address'])
                break
except Exception as e:
    print('ERROR: ' + str(e), file=sys.stderr)
" 2>/dev/null || true)
 
    if [[ -z "$entries" ]]; then
        log "INFO" "Senkronize edilecek instance bulunamadı"
        return 0
    fi
 
    local count=0
    while IFS=' ' read -r name ip; do
        [[ -z "$name" || -z "$ip" ]] && continue
        local fqdn="${name}.${DNS_ZONE}"
        log "INFO" "Senkronize ediliyor: $fqdn → $ip"
        dns_add "$fqdn" "$ip" && (( count++ )) || true
    done <<< "$entries"
 
    log "INFO" "Senkronizasyon tamamlandı: $count kayıt işlendi"
}
 
# =============================================================================
# Event parse
# =============================================================================
 
parse_event() {
    local json="$1"
    python3 -c "
import sys, json
try:
    d = json.loads(sys.argv[1])
    meta = d.get('metadata', {})
    action = meta.get('action', '')
    source = meta.get('name', '')
    if not source:
        source = meta.get('source', '').split('?')[0].split('/')[-1]
    if action and source:
        print(action + ' ' + source)
except: pass
" "$json" 2>/dev/null || echo ""
}
 

# =============================================================================
# Ana event döngüsü
# =============================================================================
 
main() {
    _acquire_lock
 
    log "INFO" "================================================"
    log "INFO" "incus-dns-sync başlatılıyor (PID: $$)"
    log "INFO" "Zone    : $DNS_ZONE"
    log "INFO" "API     : $TECHNITIUM_API"
    log "INFO" "Log     : $LOG_FILE"
    log "INFO" "================================================"
 
    # Bağımlılık kontrolü
    for dep in incus curl python3; do
        if ! command -v "$dep" &>/dev/null; then
            log "FATAL" "Bağımlılık eksik: $dep"
            _release_lock
            exit 1
        fi
    done
 
    # Technitium erişim kontrolü
    if ! _technitium_call "/api/user/session/get" > /dev/null 2>&1; then
        log "WARN" "Technitium API şu an erişilemiyor — devam ediliyor"
    fi
 
    # Mevcut instance'ları senkronize et
    sync_existing
 
    log "INFO" "Lifecycle event dinleyici başlatılıyor..."
 
    # incus monitor JSON satır satır yayar
    incus monitor --type=lifecycle --format=json 2>/dev/null | while IFS= read -r line; do
        [[ -z "$line" ]] && continue
 
        local parsed
        parsed=$(parse_event "$line")
        [[ -z "$parsed" ]] && continue
 
        local action source
        action="${parsed%% *}"
        source="${parsed#* }"
 
        log "DEBUG" "Event alındı: action=$action source=$source"
 
        case "$action" in
            instance-started)
                handle_started "$source" &
                ;;
            instance-deleted)
                handle_deleted "$source" &
                ;;
            instance-stopped)
                handle_stopped "$source"
                ;;
            *)
                log "DEBUG" "İşlenmeyen event: $action ($source)"
                ;;
        esac
 
    done
 
    # incus monitor beklenmedik şekilde kapandıysa
    log "ERROR" "incus monitor akışı kesildi — servis yeniden başlatılacak"
    _release_lock
    exit 1
}
 
main "$@"