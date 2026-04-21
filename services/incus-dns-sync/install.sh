#!/usr/bin/env bash
# =============================================================================
# install.sh — incus-dns-sync kurulum scripti
# Çalıştır: sudo bash install.sh
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_NAME="incus-dns-sync"
INSTALL_DIR="/opt/infra/services/${SERVICE_NAME}"
CONFIG_DIR="/etc/${SERVICE_NAME}"
LOG_DIR="/var/log/${SERVICE_NAME}"

# --- Renk çıktısı ---
_green() { echo -e "\033[32m$*\033[0m"; }
_yellow() { echo -e "\033[33m$*\033[0m"; }
_red() { echo -e "\033[31m$*\033[0m"; }

if [[ $EUID -ne 0 ]]; then
    _red "Hata: Bu script root olarak çalıştırılmalı (sudo bash install.sh)"
    exit 1
fi

echo ""
_green "=== incus-dns-sync kurulumu ==="
echo ""

# 1. Dizinleri oluştur
echo "→ Dizinler oluşturuluyor..."
mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" "$LOG_DIR"

# 2. Script ve service dosyalarını kopyala
echo "→ Dosyalar kopyalanıyor..."
cp "${SCRIPT_DIR}/${SERVICE_NAME}.sh"      "${INSTALL_DIR}/${SERVICE_NAME}.sh"
chmod 750 "${INSTALL_DIR}/${SERVICE_NAME}.sh"

# Config sadece yoksa kopyala — mevcut yapılandırmanın üstüne yazma
if [[ ! -f "${CONFIG_DIR}/${SERVICE_NAME}.conf" ]]; then
    cp "${SCRIPT_DIR}/${SERVICE_NAME}.conf" "${CONFIG_DIR}/${SERVICE_NAME}.conf"
    chmod 640 "${CONFIG_DIR}/${SERVICE_NAME}.conf"
    _yellow "  ⚠  Config kopyalandı: ${CONFIG_DIR}/${SERVICE_NAME}.conf"
    _yellow "     Token ve domain bilgilerini düzenle, sonra servisi başlat."
else
    echo "  Config zaten mevcut, üstüne yazılmadı: ${CONFIG_DIR}/${SERVICE_NAME}.conf"
fi

# 3. systemd unit
echo "→ systemd unit kuruluyor..."
cp "${SCRIPT_DIR}/${SERVICE_NAME}.service" "/etc/systemd/system/${SERVICE_NAME}.service"
systemctl daemon-reload
systemctl enable "${SERVICE_NAME}.service"

echo ""
_green "=== Kurulum tamamlandı ==="
echo ""
echo "Sonraki adımlar:"
echo "  1. Config dosyasını düzenle:"
echo "       nano ${CONFIG_DIR}/${SERVICE_NAME}.conf"
echo ""
echo "  2. Servisi başlat:"
echo "       systemctl start ${SERVICE_NAME}"
echo ""
echo "  3. Logları izle:"
echo "       journalctl -fu ${SERVICE_NAME}"
echo "       tail -f ${LOG_DIR}/${SERVICE_NAME}.log"
echo ""
echo "  4. Hızlı test (manuel sync):"
echo "       ${INSTALL_DIR}/${SERVICE_NAME}.sh"
echo ""