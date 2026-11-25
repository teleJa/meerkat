package harnessweb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"meerkat/edge/internal/filelock"
)

type TrustOwner struct {
	DeviceID    string `json:"device_id"`
	Fingerprint string `json:"secret_fp"`
}

func NewTrustOwner(id, secret string) TrustOwner {
	sum := sha256.Sum256([]byte(secret))
	return TrustOwner{id, hex.EncodeToString(sum[:8])}
}

type trustLease struct {
	TrustOwner
	Profile   string `json:"profile"`
	Authority string `json:"authority"`
}
type trustLedger struct {
	Leases   map[string]trustLease
	Blocks   map[string]string
	Original map[string][]byte `json:",omitempty"`
	Created  map[string]bool   `json:",omitempty"`
}

func (l trustLedger) hosts(profile string) []string {
	unique := map[string]bool{}
	for _, lease := range l.Leases {
		if lease.Profile == profile {
			unique[lease.Authority] = true
		}
	}
	hosts := make([]string, 0, len(unique))
	for host := range unique {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

type patchChange struct {
	Profile       string
	Before, After []byte
}
type trustTransaction struct {
	Ledger  trustLedger
	Changes []patchChange
}

func readOptional(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}
func atomicTrustWrite(path string, data []byte) error {
	current, err := readOptional(path)
	if err != nil || bytes.Equal(current, data) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".meerkat-trust-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for i := 0; i < 8; i++ {
		err = os.Rename(f.Name(), path)
		if err == nil || runtime.GOOS != "windows" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	return err
}
func patchFile(home, profile string) (string, error) {
	if !profileNamePattern.MatchString(profile) || strings.Contains(profile, "..") {
		return "", errors.New("无效的 profile")
	}
	dir := filepath.Join(ProfilesDir(home), profile)
	path := filepath.Join(dir, "cordis.patch.yml")
	for _, p := range []string{ProfilesDir(home), dir, path} {
		info, err := os.Lstat(p)
		if err != nil {
			if p == path && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("profile 补丁路径包含符号链接")
		}
	}
	return path, nil
}
func (t *trustTransaction) finish(home, ledgerPath, journal string) error {
	for _, ch := range t.Changes {
		path, err := patchFile(home, ch.Profile)
		if err != nil {
			return err
		}
		cur, err := readOptional(path)
		if err != nil {
			return err
		}
		if bytes.Equal(cur, ch.After) && (cur == nil) == (ch.After == nil) {
			continue
		}
		if !bytes.Equal(cur, ch.Before) {
			return fmt.Errorf("profile %s 在登记期间发生修改", ch.Profile)
		}
		if ch.After == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if err := atomicTrustWrite(path, ch.After); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(t.Ledger, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicTrustWrite(ledgerPath, data); err != nil {
		return err
	}
	return os.Remove(journal)
}

// A nil owner denotes an explicit local unbind. An authenticated remote
// revocation must match the current credentials as well as the state directory.
func UpdateTrust(home, stateDir string, owner *TrustOwner, profile, rawURL string) error {
	authority, err := trustedAuthority(rawURL)
	if err != nil {
		return err
	}
	key, err := filepath.Abs(stateDir)
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(key); err == nil {
		key = real
	}
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	dir := filepath.Join(home, "runtime")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "meerkat-trusted-hosts.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	until := time.Now().Add(10 * time.Second)
	for {
		ok, err := filelock.Try(lock)
		if err != nil {
			return err
		}
		if ok {
			break
		}
		if time.Now().After(until) {
			return errors.New("等待信任账本超时")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ledgerPath := filepath.Join(dir, "meerkat-trusted-hosts.json")
	journal := ledgerPath + ".pending"
	data, err := readOptional(journal)
	if err != nil {
		return err
	}
	if data != nil {
		var pending trustTransaction
		if err := json.Unmarshal(data, &pending); err != nil {
			return err
		}
		if err := pending.finish(home, ledgerPath, journal); err != nil {
			return err
		}
	}
	ledger := trustLedger{Leases: map[string]trustLease{}, Blocks: map[string]string{}, Created: map[string]bool{}, Original: map[string][]byte{}}
	data, err = readOptional(ledgerPath)
	if err != nil {
		return err
	}
	if data != nil {
		if err := json.Unmarshal(data, &ledger); err != nil {
			return err
		}
	}
	if ledger.Leases == nil || ledger.Blocks == nil {
		return errors.New("信任账本损坏")
	}
	previous, exists := ledger.Leases[key]
	profiles := map[string]bool{}
	if exists {
		profiles[previous.Profile] = true
	}
	if profile == "" && exists && owner != nil && previous.TrustOwner != *owner {
		return nil
	}
	if authority != "" {
		profiles[profile] = true
	}
	oldHosts := make(map[string][]string, len(profiles))
	for p := range profiles {
		oldHosts[p] = ledger.hosts(p)
	}
	if authority != "" {
		if owner == nil || owner.DeviceID == "" || profile == "" {
			return errors.New("信任登记缺少设备身份或 profile")
		}
		ledger.Leases[key] = trustLease{*owner, profile, authority}
	} else {
		delete(ledger.Leases, key)
	}
	tx := trustTransaction{Ledger: ledger}
	for p := range profiles {
		hosts := ledger.hosts(p)
		if slices.Equal(oldHosts[p], hosts) {
			continue
		}
		path, err := patchFile(home, p)
		if err != nil {
			return err
		}
		cur, err := readOptional(path)
		if err != nil {
			return err
		}
		base := cur
		if block := ledger.Blocks[p]; block != "" {
			original, restored := ledger.Original[p]
			if bytes.HasSuffix(cur, []byte(block)) && bytes.Count(cur, []byte(block)) == 1 && (!restored || string(cur) == block) {
				base = bytes.Replace(cur, []byte(block), original, 1)
			} else {
				base, err = removeSerializedTrust(cur, block, oldHosts[p])
				if err != nil {
					return fmt.Errorf("profile %s: %w", p, err)
				}
			}

			delete(ledger.Original, p)
		}
		patchBase := base
		var rewritten bool
		if len(hosts) > 0 {
			patchBase, rewritten, err = appendableBase(base)
			if err != nil {
				return err
			}
		}
		block, err := trustBlock(patchBase, hosts)
		if err != nil {
			return err
		}
		if block == "" {
			delete(ledger.Blocks, p)
		} else {
			if rewritten {
				ledger.Blocks[p] = string(patchBase) + block
				ledger.Original[p] = base
			} else {
				ledger.Blocks[p] = block
			}
		}
		next := append(bytes.Clone(patchBase), block...)
		if block != "" && cur == nil {
			ledger.Created[p] = true
		}
		if block == "" {
			if ledger.Created[p] && len(next) == 0 {
				next = nil
			}
			delete(ledger.Created, p)
		}
		if !bytes.Equal(next, cur) {
			tx.Changes = append(tx.Changes, patchChange{p, cur, next})
		}
	}
	encoded, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	if len(tx.Changes) == 0 && (bytes.Equal(encoded, data) || data == nil && len(ledger.Leases) == 0) {
		return nil
	}
	intent, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	if err := atomicTrustWrite(journal, intent); err != nil {
		return err
	}
	return tx.finish(home, ledgerPath, journal)
}
