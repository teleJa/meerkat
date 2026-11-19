package hub

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	MobileScopeDevices = "devices:read"
	MobileScopeTasks   = "tasks:read"
	MobileScopeHarness = "harness:proxy"

	MobileCredentialTTL    = 90 * 24 * time.Hour
	MobileRotationGrace    = 5 * time.Minute
	MobileExpiryWarningFor = 7 * 24 * time.Hour
	MobilePairingTTL       = 10 * time.Minute

	mobileTokenPrefix   = "mkm_"
	mobileCredentialID  = "mc_"
	mobilePairingPrefix = "MEERKAT"
)

var (
	ErrMobilePairingInvalid    = errors.New("mobile pairing code is invalid, used or expired")
	ErrMobileCredentialInvalid = errors.New("mobile credential is invalid")
	ErrMobileCredentialExpired = errors.New("mobile credential is expired")
)

// MobileScopes is the fixed, read-only scope set for M3 credentials.
var MobileScopes = []string{MobileScopeDevices, MobileScopeTasks, MobileScopeHarness}

type MobileCredentialRow struct {
	ID                 string
	SecretHash         string
	PreviousSecretHash *string
	PreviousValidUntil *string
	Label              string
	Platform           string
	Model              string
	AppVersion         string
	Scopes             string
	CreatedAt          string
	LastUsedAt         *string
	ExpiresAt          string
}

type MobileCredential struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Platform     string   `json:"platform"`
	Model        string   `json:"model"`
	AppVersion   string   `json:"app_version"`
	Scopes       []string `json:"scopes"`
	CreatedAt    string   `json:"created_at"`
	LastUsedAt   *string  `json:"last_used_at"`
	ExpiresAt    string   `json:"expires_at"`
	Status       string   `json:"status"`
	DaysToExpiry *int     `json:"days_to_expiry,omitempty"`
}

type mobileRegistration struct {
	Code       string `json:"code"`
	Label      string `json:"label"`
	Platform   string `json:"platform"`
	Model      string `json:"model"`
	AppVersion string `json:"app_version"`
}

func mobileCredentialStatus(row MobileCredentialRow, now time.Time) (string, *int) {
	expiry, err := time.Parse(time.RFC3339Nano, row.ExpiresAt)
	if err != nil || !expiry.After(now) {
		return "expired", nil
	}
	days := int(expiry.Sub(now).Hours() / 24)
	if days < 0 {
		days = 0
	}
	if expiry.Sub(now) <= MobileExpiryWarningFor {
		return "expiring", &days
	}
	return "active", &days
}

func mobileCredentialToDTO(row MobileCredentialRow, now time.Time) MobileCredential {
	var scopes []string
	_ = json.Unmarshal([]byte(row.Scopes), &scopes)
	if scopes == nil {
		scopes = []string{}
	}
	status, days := mobileCredentialStatus(row, now)
	return MobileCredential{
		ID: row.ID, Label: row.Label, Platform: row.Platform, Model: row.Model,
		AppVersion: row.AppVersion, Scopes: scopes, CreatedAt: row.CreatedAt,
		LastUsedAt: row.LastUsedAt, ExpiresAt: row.ExpiresAt,
		Status: status, DaysToExpiry: days,
	}
}

func scanMobileCredential(scanner interface{ Scan(dest ...any) error }) (MobileCredentialRow, error) {
	var row MobileCredentialRow
	var previousHash, previousUntil, lastUsed sql.NullString
	err := scanner.Scan(
		&row.ID, &row.SecretHash, &previousHash, &previousUntil, &row.Label,
		&row.Platform, &row.Model, &row.AppVersion, &row.Scopes, &row.CreatedAt,
		&lastUsed, &row.ExpiresAt,
	)
	row.PreviousSecretHash = nullString(previousHash)
	row.PreviousValidUntil = nullString(previousUntil)
	row.LastUsedAt = nullString(lastUsed)
	return row, err
}

func (h *Hub) getMobileCredential(id string) (*MobileCredentialRow, error) {
	row := h.db.QueryRow(
		`SELECT id, secret_hash, previous_secret_hash, previous_valid_until, label,
		        platform, model, app_version, scopes, created_at, last_used_at, expires_at
		   FROM mobile_credentials WHERE id = ?`, id)
	credential, err := scanMobileCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

// parseMobileToken splits mkm_<credential id>_<secret> at the last underscore.
func parseMobileToken(token string) (id, secret string, ok bool) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, mobileTokenPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(token, mobileTokenPrefix)
	if !strings.HasPrefix(rest, mobileCredentialID) {
		return "", "", false
	}
	// base64url 的 secret 本身可以包含下划线，分隔符必须取 id 前缀之后
	// 的第一个下划线，而不是整个 token 的最后一个。
	offset := strings.Index(rest[len(mobileCredentialID):], "_")
	if offset <= 0 {
		return "", "", false
	}
	index := len(mobileCredentialID) + offset
	if index == len(rest)-1 {
		return "", "", false
	}
	id = rest[:index]
	secret = rest[index+1:]
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(decoded) != 32 {
		return "", "", false
	}
	return id, secret, true
}

func newMobileToken(id string) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return mobileTokenPrefix + id + "_" + base64.RawURLEncoding.EncodeToString(secret), nil
}

// authenticateMobile validates a bearer token and returns its granted scopes.
func (h *Hub) authenticateMobile(token string, now time.Time) (*AuthInfo, error) {
	id, secret, ok := parseMobileToken(token)
	if !ok {
		return nil, ErrMobileCredentialInvalid
	}
	credential, err := h.getMobileCredential(id)
	if err != nil {
		return nil, err
	}
	if credential == nil {
		// 行已被删除（撤销即删）。返回 Invalid 而不是 Revoked：
		// 服务器已经不记得这个 id 了，无从区分「撤销」和「从不存在」。
		return nil, ErrMobileCredentialInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, credential.ExpiresAt)
	if err != nil || !expiry.After(now) {
		return nil, ErrMobileCredentialExpired
	}
	hash := tokenHash(secret)
	matched := subtle.ConstantTimeCompare([]byte(hash), []byte(credential.SecretHash)) == 1
	if !matched && credential.PreviousSecretHash != nil && credential.PreviousValidUntil != nil {
		until, parseErr := time.Parse(time.RFC3339Nano, *credential.PreviousValidUntil)
		if parseErr == nil && until.After(now) {
			matched = subtle.ConstantTimeCompare([]byte(hash), []byte(*credential.PreviousSecretHash)) == 1
		}
	}
	if !matched {
		return nil, ErrMobileCredentialInvalid
	}
	var scopes []string
	_ = json.Unmarshal([]byte(credential.Scopes), &scopes)
	// Throttle last_used_at writes: a long-lived DSH page can issue many
	// requests, and every authenticated request would otherwise write SQLite.
	stale := credential.LastUsedAt == nil
	if !stale {
		if previous, parseErr := time.Parse(time.RFC3339Nano, *credential.LastUsedAt); parseErr != nil || now.Sub(previous) > 30*time.Second {
			stale = true
		}
	}
	if stale {
		_, _ = h.db.Exec("UPDATE mobile_credentials SET last_used_at = ? WHERE id = ?", nowISO(now), id)
	}
	return &AuthInfo{Via: "mobile", CredentialID: id, Scopes: scopes, ExpiresAt: credential.ExpiresAt}, nil
}

func normalizeMobilePairingCode(raw string) string {
	value := strings.ToUpper(strings.TrimSpace(raw))
	value = strings.NewReplacer("-", "", " ", "", "\t", "", "\n", "").Replace(value)
	value = strings.TrimPrefix(value, mobilePairingPrefix)
	return value
}

func mobilePairingHash(raw string) string {
	return tokenHash("meerkat.mobile-pairing:" + normalizeMobilePairingCode(raw))
}

func randomPairingCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no I/O/0/1
	buf := make([]byte, 8)
	random := make([]byte, len(buf))
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = alphabet[int(random[i])%len(alphabet)]
	}
	body := string(buf)
	return mobilePairingPrefix + "-" + body[:4] + "-" + body[4:], nil
}

func (h *Hub) createMobilePairingCode(label *string, ttl time.Duration, now time.Time) (string, string, error) {
	code, err := randomPairingCode()
	if err != nil {
		return "", "", err
	}
	expiresAt := nowISO(now.Add(ttl))
	_, err = h.db.Exec(
		`INSERT INTO mobile_pairing_codes (code_hash, label, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		mobilePairingHash(code), label, nowISO(now), expiresAt,
	)
	if err != nil {
		return "", "", err
	}
	return code, expiresAt, nil
}

func (h *Hub) registerMobileCredential(input mobileRegistration, now time.Time) (MobileCredential, string, error) {
	codeHash := mobilePairingHash(input.Code)
	tx, err := h.db.Begin()
	if err != nil {
		return MobileCredential{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	var expiresAt string
	var usedAt sql.NullString
	if err := tx.QueryRow(
		`SELECT expires_at, used_at FROM mobile_pairing_codes WHERE code_hash = ?`, codeHash,
	).Scan(&expiresAt, &usedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MobileCredential{}, "", ErrMobilePairingInvalid
		}
		return MobileCredential{}, "", err
	}
	if usedAt.Valid {
		return MobileCredential{}, "", ErrMobilePairingInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expiry.After(now) {
		return MobileCredential{}, "", ErrMobilePairingInvalid
	}
	id := mobileCredentialID + randomHex(4)
	token, err := newMobileToken(id)
	if err != nil {
		return MobileCredential{}, "", err
	}
	_, secret, _ := parseMobileToken(token)
	label := strings.TrimSpace(input.Label)
	if label == "" {
		label = strings.TrimSpace(input.Model)
	}
	if label == "" {
		label = "移动设备"
	}
	if len(label) > 120 {
		label = label[:120]
	}
	scopesJSON, _ := json.Marshal(MobileScopes)
	credentialExpiry := nowISO(now.Add(MobileCredentialTTL))
	if _, err := tx.Exec(
		`INSERT INTO mobile_credentials
		 (id, secret_hash, label, platform, model, app_version, scopes, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, tokenHash(secret), label, strings.TrimSpace(input.Platform), strings.TrimSpace(input.Model),
		strings.TrimSpace(input.AppVersion), string(scopesJSON), nowISO(now), credentialExpiry,
	); err != nil {
		return MobileCredential{}, "", err
	}
	if _, err := tx.Exec(
		`UPDATE mobile_pairing_codes SET used_at = ?, used_credential_id = ? WHERE code_hash = ?`,
		nowISO(now), id, codeHash,
	); err != nil {
		return MobileCredential{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return MobileCredential{}, "", err
	}
	row := MobileCredentialRow{
		ID: id, Label: label, Platform: strings.TrimSpace(input.Platform),
		Model: strings.TrimSpace(input.Model), AppVersion: strings.TrimSpace(input.AppVersion),
		Scopes: string(scopesJSON), CreatedAt: nowISO(now), ExpiresAt: credentialExpiry,
	}
	detail, _ := json.Marshal(map[string]any{"label": label, "platform": row.Platform})
	detailStr := string(detail)
	h.logEvent("mobile.credential.created", nil, &detailStr)
	return mobileCredentialToDTO(row, now), token, nil
}

// listMobileCredentials 返回库里现存的全部设备凭证。
// 撤销是硬删除，所以表里本来就没有「已撤销」的行，不需要过滤条件。
// 已过期的凭证仍在列表里——用户要能看到它过期了，才有「重新配对」的判断依据。
func (h *Hub) listMobileCredentials() ([]MobileCredential, error) {
	rows, err := h.db.Query(
		`SELECT id, secret_hash, previous_secret_hash, previous_valid_until, label,
		        platform, model, app_version, scopes, created_at, last_used_at, expires_at
		   FROM mobile_credentials ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	var credentials []MobileCredential
	for rows.Next() {
		row, err := scanMobileCredential(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, mobileCredentialToDTO(row, now))
	}
	if credentials == nil {
		credentials = []MobileCredential{}
	}
	return credentials, rows.Err()
}

// revokeMobileCredential 撤销一台移动设备——直接把这行删掉。
// 撤销是永久操作，行一旦删除不可恢复；留痕靠 events 表的
// mobile.credential.revoked 事件，不靠保留数据行。
func (h *Hub) revokeMobileCredential(id string) (bool, error) {
	result, err := h.db.Exec(`DELETE FROM mobile_credentials WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	h.logEvent("mobile.credential.revoked", nil, &id)
	return true, nil
}

func (h *Hub) rotateMobileCredential(id string, now time.Time) (string, error) {
	credential, err := h.getMobileCredential(id)
	if err != nil || credential == nil {
		return "", ErrMobileCredentialInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, credential.ExpiresAt)
	if err != nil || !expiry.After(now) {
		return "", ErrMobileCredentialExpired
	}
	token, err := newMobileToken(id)
	if err != nil {
		return "", err
	}
	_, secret, _ := parseMobileToken(token)
	previousUntil := nowISO(now.Add(MobileRotationGrace))
	if _, err := h.db.Exec(
		`UPDATE mobile_credentials
		    SET secret_hash = ?, previous_secret_hash = secret_hash,
		        previous_valid_until = ?, last_used_at = ?
		  WHERE id = ?`,
		tokenHash(secret), previousUntil, nowISO(now), id,
	); err != nil {
		return "", err
	}
	h.logEvent("mobile.credential.rotated", nil, &id)
	return token, nil
}
