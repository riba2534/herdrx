package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/riba2534/herdrx/internal/hostruntime"
	"github.com/riba2534/herdrx/internal/secure"
	"github.com/riba2534/herdrx/internal/store"
	"golang.org/x/crypto/ssh"
)

func validResourceName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 80 && !strings.ContainsFunc(name, unicode.IsControl)
}

func (a *API) sshKeyRoutes(r chi.Router) {
	r.Get("/", a.listSSHKeys)
	r.With(a.requireCSRF).Post("/", a.createSSHKey)
	r.With(a.requireCSRF).Patch("/{keyID}", a.updateSSHKey)
	r.With(a.requireCSRF).Delete("/{keyID}", a.deleteSSHKey)
}

type sshKeyRequest struct {
	Name        string  `json:"name"`
	Generate    bool    `json:"generate"`
	PrivateKey  *string `json:"private_key"`
	Passphrase  *string `json:"passphrase"`
	PublicKey   *string `json:"public_key"`
	Certificate *string `json:"certificate"`
	Revision    int     `json:"revision"`
}

func (a *API) listSSHKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.store.ListSSHKeys(r.Context(), userFromContext(r.Context()).ID)
	if err != nil {
		writeError(w, 500, "database_error", "无法读取密钥，请重试")
		return
	}
	writeJSON(w, 200, map[string]any{"keys": keys})
}

func materialMetadata(k *store.SSHKey, m secure.SSHKeyMaterial, public *string) error {
	signer, encrypted, err := m.Parse()
	if err != nil {
		return err
	}
	key := secure.SSHSignerPublicKey(signer)
	if public != nil && strings.TrimSpace(*public) != "" {
		p, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(*public))
		if err != nil || len(bytes.TrimSpace(rest)) != 0 || !bytes.Equal(p.Marshal(), key.Marshal()) {
			return fmt.Errorf("公钥与私钥不匹配；留空可自动提取公钥")
		}
	}
	k.PublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	k.Fingerprint = ssh.FingerprintSHA256(key)
	k.Algorithm = key.Type()
	k.Encrypted = encrypted
	k.Certificate = strings.TrimSpace(m.Certificate)
	return nil
}

func (a *API) createSSHKey(w http.ResponseWriter, r *http.Request) {
	var input sshKeyRequest
	if decodeJSON(r, &input) != nil {
		writeError(w, 400, "invalid_request", "请填写有效的密钥信息")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validResourceName(input.Name) {
		writeError(w, 400, "invalid_name", "密钥名称须为 1–80 个字符，不能包含换行或控制字符")
		return
	}
	id, err := secure.Token(12)
	if err != nil {
		writeError(w, 500, "key_error", "无法创建密钥，请重试")
		return
	}
	k := store.SSHKey{ID: "key_" + id, OwnerID: userFromContext(r.Context()).ID, Name: input.Name}
	m := secure.SSHKeyMaterial{}
	if input.Generate {
		if input.PrivateKey != nil && *input.PrivateKey != "" {
			writeError(w, 400, "invalid_request", "生成密钥时无需填写私钥")
			return
		}
		private, _, err := secure.GenerateSSHKey("")
		if err != nil {
			writeError(w, 500, "key_error", "无法生成密钥，请重试")
			return
		}
		m.PrivateKey = string(private)
	} else if input.PrivateKey != nil {
		m.PrivateKey = *input.PrivateKey
	}
	if input.Passphrase != nil {
		m.Passphrase = *input.Passphrase
	}
	if input.Certificate != nil {
		m.Certificate = *input.Certificate
	}
	if err = materialMetadata(&k, m, input.PublicKey); err != nil {
		writeError(w, 400, "invalid_key", err.Error())
		return
	}
	plaintext, _ := json.Marshal(m)
	ciphertext, err := hostruntime.SealCredential(a.vault, k.OwnerID, k.ID, "saved_key", plaintext)
	if err == nil {
		err = a.store.CreateSSHKey(r.Context(), k, store.Credential{ID: k.ID, OwnerID: k.OwnerID, Kind: "saved_key", Ciphertext: ciphertext})
	}
	if err != nil {
		writeError(w, 500, "database_error", "无法保存密钥，请重试")
		return
	}
	k, err = a.store.SSHKeyByID(r.Context(), k.OwnerID, k.ID)
	if err != nil {
		writeError(w, 500, "database_error", "无法读取已保存的密钥")
		return
	}
	a.audit(r, "ssh_key.created", "ssh_key", k.ID, map[string]string{"name": k.Name, "fingerprint": k.Fingerprint})
	writeJSON(w, 201, map[string]any{"key": k})
}

func (a *API) updateSSHKey(w http.ResponseWriter, r *http.Request) {
	k, err := a.store.SSHKeyByID(r.Context(), userFromContext(r.Context()).ID, chi.URLParam(r, "keyID"))
	if err != nil {
		writeError(w, 404, "key_not_found", "密钥不存在或无权访问")
		return
	}
	var input sshKeyRequest
	if decodeJSON(r, &input) != nil || input.Generate {
		writeError(w, 400, "invalid_request", "请填写有效的密钥信息")
		return
	}
	k.Name = strings.TrimSpace(input.Name)
	if !validResourceName(k.Name) {
		writeError(w, 400, "invalid_name", "密钥名称须为 1–80 个字符")
		return
	}
	if input.Revision != k.Revision {
		writeError(w, 409, "key_changed", "密钥已被修改，请刷新后重试")
		return
	}
	changed := input.PrivateKey != nil || input.Passphrase != nil || input.Certificate != nil || input.PublicKey != nil
	var ciphertext []byte
	if changed {
		c, err := a.store.CredentialByID(r.Context(), k.OwnerID, k.ID)
		if err != nil {
			writeError(w, 500, "key_error", "无法读取密钥，请重试")
			return
		}
		plain, err := hostruntime.OpenCredential(a.vault, k.OwnerID, k.ID, c.Kind, c.Ciphertext)
		var m secure.SSHKeyMaterial
		if err != nil || json.Unmarshal(plain, &m) != nil {
			writeError(w, 500, "key_error", "无法解密密钥，请检查工作台主机密钥配置")
			return
		}
		if input.PrivateKey != nil {
			m.PrivateKey = *input.PrivateKey
			m.Passphrase = ""
		}
		if input.Passphrase != nil {
			m.Passphrase = *input.Passphrase
		}
		if input.Certificate != nil {
			m.Certificate = *input.Certificate
		}
		if err = materialMetadata(&k, m, input.PublicKey); err != nil {
			writeError(w, 400, "invalid_key", err.Error())
			return
		}
		plain, _ = json.Marshal(m)
		ciphertext, err = hostruntime.SealCredential(a.vault, k.OwnerID, k.ID, c.Kind, plain)
		if err != nil {
			writeError(w, 500, "key_error", "无法保存密钥，请重试")
			return
		}
	}
	if err = a.store.UpdateSSHKey(r.Context(), k, ciphertext); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, 409, "key_changed", "密钥已被修改，请刷新后重试")
		} else {
			writeError(w, 500, "database_error", "无法保存密钥，请重试")
		}
		return
	}
	if changed {
		hosts, err := a.store.ListHosts(r.Context(), k.OwnerID)
		if err == nil {
			for _, h := range hosts {
				if h.CredentialID == k.ID {
					a.hosts.CloseHost(h.ID)
				}
			}
		}
	}
	k, err = a.store.SSHKeyByID(r.Context(), k.OwnerID, k.ID)
	if err != nil {
		writeError(w, 500, "database_error", "无法读取已保存的密钥")
		return
	}
	a.audit(r, "ssh_key.updated", "ssh_key", k.ID, map[string]any{"name": k.Name, "fingerprint": k.Fingerprint, "material_changed": changed})
	writeJSON(w, 200, map[string]any{"key": k})
}

func (a *API) deleteSSHKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "keyID")
	err := a.store.DeleteSSHKey(r.Context(), userFromContext(r.Context()).ID, id)
	if errors.Is(err, store.ErrKeyInUse) {
		writeError(w, 409, "key_in_use", "仍有主机使用此密钥，请先修改这些主机的认证方式")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "key_not_found", "密钥不存在或无权访问")
		return
	}
	if err != nil {
		writeError(w, 500, "database_error", "无法删除密钥，请重试")
		return
	}
	a.audit(r, "ssh_key.deleted", "ssh_key", id, nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
