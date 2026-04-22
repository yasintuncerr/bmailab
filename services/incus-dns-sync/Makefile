BINARY     := incus-dns-sync
BUILD_DIR  := build
CMD_PATH   := ./cmd/incus-dns-sync

# go binary — sudo altında PATH daraldığı için tam yolu tercih et
GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)

# Build hedef mimarisi (cross-compile için override edilebilir)
GOOS   ?= linux
GOARCH ?= amd64

.PHONY: all build install clean

all: build

build:
	@echo "→ Derleniyor: $(BINARY) ($(GOOS)/$(GOARCH))"
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build -trimpath -ldflags="-s -w" \
		-o $(BUILD_DIR)/$(BINARY) $(CMD_PATH)
	@echo "  Binary: $(BUILD_DIR)/$(BINARY)"

install: build
	@echo "→ Kuruluyor..."
	install -m 755 $(BUILD_DIR)/$(BINARY) /usr/local/bin/$(BINARY)

	@mkdir -p /etc/incus-dns-sync
	@if [ ! -f /etc/incus-dns-sync/incus-dns-sync.yaml ]; then \
		install -m 640 incus-dns-sync.yaml /etc/incus-dns-sync/incus-dns-sync.yaml; \
		echo "  ⚠  Config kopyalandı: /etc/incus-dns-sync/incus-dns-sync.yaml"; \
		echo "     Token ve domain bilgilerini düzenle, sonra servisi başlat."; \
	else \
		echo "  Config zaten mevcut, üstüne yazılmadı."; \
	fi

	install -m 644 incus-dns-sync.service /etc/systemd/system/incus-dns-sync.service
	systemctl daemon-reload
	systemctl enable incus-dns-sync.service

	@echo ""
	@echo "Kurulum tamamlandı. Sonraki adımlar:"
	@echo "  1. nano /etc/incus-dns-sync/incus-dns-sync.yaml"
	@echo "  2. incus profile create dns-enabled"
	@echo "  3. systemctl start incus-dns-sync"
	@echo "  4. journalctl -fu incus-dns-sync"

clean:
	rm -rf $(BUILD_DIR)





TECHNITIUM_API="http://10.14.21.20:5380"
TECHNITIUM_TOKEN="705acf2cb10637723b35bdf7ff8f266fa5d0d29173e63b902f9cb9842c5d0900"
