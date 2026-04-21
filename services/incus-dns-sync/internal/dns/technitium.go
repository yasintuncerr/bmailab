package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	base  string
	token string
	http  *http.Client
	zone  string
	ttl   int
}

func New(apiBase, token, zone string, ttl int, timeout time.Duration) *Client {
	return &Client{
		base:  apiBase,
		token: token,
		zone:  zone,
		ttl:   ttl,
		http:  &http.Client{Timeout: timeout},
	}
}

func (c *Client) ZoneRecords(ctx context.Context) (map[string]string, error) {
	params := url.Values{
		"token":  {c.token},
		"domain": {c.zone},
		"type":   {"A"},
	}

	body, err := c.get(ctx, "/api/zones/records/get", params)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Status   string `json:"status"`
		Response struct {
			Records []struct {
				Name  string `json:"name"`
				RData struct {
					IPAddress string `json:"ipAddress"`
				} `json:"rData"`
			} `json:"records"`
		} `json:"response"`
		ErrorMessage string `json:"errorMessage"`
	}

	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("zone records parse hatası: %w", err)
	}
	if resp.Status != "ok" {
		return nil, fmt.Errorf("technitium hata: %s", resp.ErrorMessage)
	}

	records := make(map[string]string, len(resp.Response.Records))
	for _, r := range resp.Response.Records {
		if r.Name != "" && r.RData.IPAddress != "" {
			records[r.Name] = r.RData.IPAddress
		}
	}
	return records, nil
}

func (c *Client) UpsertA(ctx context.Context, fqdn, ip string) error {
	params := url.Values{
		"token":     {c.token},
		"domain":    {fqdn},
		"type":      {"A"},
		"ipAddress": {ip},
		"ttl":       {fmt.Sprintf("%d", c.ttl)},
		"overwrite": {"true"},
	}

	body, err := c.get(ctx, "/api/zones/records/add", params)
	if err != nil {
		return err
	}
	return c.checkStatus(body)
}

func (c *Client) DeleteA(ctx context.Context, fqdn, ip string) error {
	params := url.Values{
		"token":     {c.token},
		"domain":    {fqdn},
		"type":      {"A"},
		"ipAddress": {ip},
	}

	body, err := c.get(ctx, "/api/zones/records/delete", params)
	if err != nil {
		return err
	}
	return c.checkStatus(body)
}

func (c *Client) Ping(ctx context.Context) error {
	params := url.Values{"token": {c.token}}
	body, err := c.get(ctx, "/api/user/session/get", params)
	if err != nil {
		return err
	}
	return c.checkStatus(body)
}

func (c *Client) get(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	reqURL := c.base + endpoint + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("istek oluşturulamadı: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("technitium API ulaşılamıyor (%s): %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("technitium HTTP %d (%s)", resp.StatusCode, endpoint)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("yanıt okunamadı: %w", err)
	}
	return data, nil
}

func (c *Client) checkStatus(body []byte) error {
	var resp struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"errorMessage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("yanıt parse hatası: %w", err)
	}
	if resp.Status != "ok" {
		return fmt.Errorf("technitium hata: %s", resp.ErrorMessage)
	}
	return nil
}

func FQDN(instanceName, zone string) string {
	return instanceName + "." + zone
}

func SubdomainFromFQDN(fqdn, zone string) string {
	suffix := "." + zone
	if len(fqdn) > len(suffix) && fqdn[len(fqdn)-len(suffix):] == suffix {
		return fqdn[:len(fqdn)-len(suffix)]
	}
	return fqdn
}
