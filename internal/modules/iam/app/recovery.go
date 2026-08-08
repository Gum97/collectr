package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/platform/notify"
	"github.com/collectr/collectr/internal/platform/password"
	"github.com/collectr/collectr/internal/platform/totp"
)

// Reset limits.
const (
	// ResetTTL is short because the link travels by email, which is not a
	// confidential channel. Long enough to notice the message and act on it.
	ResetTTL = time.Hour
	// resetLimit bounds how often anyone can trigger mail to one address.
	resetLimit  = 3
	resetWindow = time.Hour

	// RecoveryCodeCount is how many one-time codes are issued with MFA.
	RecoveryCodeCount = 10
)

// RequestPasswordReset emails a reset link if the address has an account.
//
// It reports nothing back either way. A "no such account" answer would let this
// endpoint be used to test which addresses have accounts -- the same disclosure
// the login form is careful to avoid, and it would be pointless to guard one and
// not the other.
func (s *Service) RequestPasswordReset(ctx context.Context, email, ipPrefix string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return
	}

	allowed, err := s.store.RegisterResetAttempt(ctx, email, resetLimit, resetWindow)
	if err != nil {
		s.log.Error("rate limiting password reset", "error", err)
		return
	}
	if !allowed {
		s.log.Warn("password reset rate limit reached")
		return
	}

	user, found, err := s.store.FindUserByEmail(ctx, email)
	if err != nil {
		s.log.Error("looking up account for reset", "error", err)
		return
	}
	if !found || user.Status != "active" {
		return
	}

	raw, err := newToken()
	if err != nil {
		s.log.Error("generating reset token", "error", err)
		return
	}
	if err := s.store.CreateResetToken(ctx, user.ID, raw, ipPrefix, ResetTTL); err != nil {
		s.log.Error("storing reset token", "error", err)
		return
	}

	link := fmt.Sprintf("%s/reset-password?token=%s", s.baseURL, raw)
	body := "Chúng tôi nhận được yêu cầu đặt lại mật khẩu cho tài khoản này.\n\n" +
		link + "\n\nLiên kết có hiệu lực trong 1 giờ và chỉ dùng được một lần.\n" +
		"Nếu bạn không yêu cầu, hãy bỏ qua email này — mật khẩu của bạn không thay đổi."

	// Accounts with a second factor still need it here. Otherwise resetting by
	// email would step around MFA entirely, and whoever controls the mailbox
	// would control the account -- which is exactly what the second factor exists
	// to prevent.
	if user.MFAEnabled {
		body += "\n\nTài khoản này có xác thực hai lớp. Bạn sẽ cần mã từ ứng dụng " +
			"xác thực hoặc một mã dự phòng để hoàn tất."
	}

	if err := s.notifier.Send(ctx, notify.Message{
		To:      email,
		Subject: "Đặt lại mật khẩu Collectr",
		Body:    body,
	}); err != nil {
		s.log.Error("sending password reset", "error", err)
	}
}

// ResetPreview describes a reset token to the page that will use it.
type ResetPreview struct {
	Email       string `json:"email"`
	MFARequired bool   `json:"mfa_required"`
}

// PreviewReset validates a token so the form knows whether to ask for a code.
func (s *Service) PreviewReset(ctx context.Context, token string) (ResetPreview, error) {
	target, err := s.store.FindResetToken(ctx, token)
	if err != nil {
		return ResetPreview{}, err
	}
	return ResetPreview{Email: target.Email, MFARequired: target.MFAEnabled}, nil
}

// CompletePasswordReset sets a new password.
//
// Every session is closed, including any the attacker may hold: a reset is
// usually a response to suspecting exactly that.
func (s *Service) CompletePasswordReset(ctx context.Context, token, newPassword, mfaCode string) error {
	target, err := s.store.FindResetToken(ctx, token)
	if err != nil {
		return err
	}
	if err := password.Validate(newPassword); err != nil {
		return err
	}

	if target.MFAEnabled {
		if mfaCode == "" {
			return domain.ErrMFARequired
		}
		if err := s.verifySecondFactor(ctx, target.UserID, target.MFASecret, mfaCode); err != nil {
			return err
		}
	}

	hash, err := password.Hash(newPassword, password.DefaultParams)
	if err != nil {
		return err
	}
	return s.store.ConsumeResetToken(ctx, target.TokenID, target.UserID, hash)
}

// ChangePassword updates the password of someone already signed in.
func (s *Service) ChangePassword(ctx context.Context, userID, sessionID uuid.UUID, current, next string) error {
	user, err := s.store.FindUserByID(ctx, userID)
	if err != nil {
		return err
	}
	// Proving the current password is what stops a borrowed, unlocked browser
	// from becoming a permanent takeover.
	if _, err := password.Verify(current, user.PasswordHash); err != nil {
		return domain.ErrInvalidCredentials
	}
	if err := password.Validate(next); err != nil {
		return err
	}

	hash, err := password.Hash(next, password.DefaultParams)
	if err != nil {
		return err
	}
	// Other sessions go; the one making the change stays, so the user is not
	// signed out of the page they are on.
	return s.store.ChangePassword(ctx, userID, sessionID, hash)
}

// verifySecondFactor accepts either a TOTP code or an unused recovery code.
func (s *Service) verifySecondFactor(ctx context.Context, userID uuid.UUID, wrappedSecret []byte, code string) error {
	code = strings.TrimSpace(code)

	if len(wrappedSecret) > 0 {
		secret, err := s.env.OpenBytes(wrappedSecret)
		if err != nil {
			return fmt.Errorf("unwrapping mfa secret: %w", err)
		}
		if err := totp.Verify(string(secret), code, time.Now()); err == nil {
			return nil
		}
	}

	// Recovery codes are the answer to a lost phone. Without them, requiring MFA
	// on reset would turn a broken handset into a permanently unreachable
	// organisation.
	sum := sha256.Sum256([]byte(normaliseRecoveryCode(code)))
	used, err := s.store.ConsumeRecoveryCode(ctx, userID, sum[:])
	if err != nil {
		return err
	}
	if !used {
		return domain.ErrMFAInvalid
	}

	remaining, err := s.store.RemainingRecoveryCodes(ctx, userID)
	if err == nil && remaining <= 2 {
		s.log.Warn("recovery codes running out", "user_id", userID, "remaining", remaining)
	}
	return nil
}

// GenerateRecoveryCodes issues a fresh set, invalidating any previous one.
//
// The plaintext is returned once and never stored: only hashes are kept, so a
// database dump does not hand over a way past the second factor.
func (s *Service) GenerateRecoveryCodes(ctx context.Context, userID uuid.UUID) ([]string, error) {
	codes := make([]string, 0, RecoveryCodeCount)
	hashes := make([][]byte, 0, RecoveryCodeCount)

	for range RecoveryCodeCount {
		code, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
		sum := sha256.Sum256([]byte(normaliseRecoveryCode(code)))
		hashes = append(hashes, sum[:])
	}

	if err := s.store.StoreRecoveryCodes(ctx, userID, hashes); err != nil {
		return nil, err
	}
	return codes, nil
}

// RemainingRecoveryCodes reports how many are left.
func (s *Service) RemainingRecoveryCodes(ctx context.Context, userID uuid.UUID) (int, error) {
	return s.store.RemainingRecoveryCodes(ctx, userID)
}

// recoveryAlphabet excludes characters people confuse when reading a printout:
// no 0/O, no 1/I/l.
const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// newRecoveryCode returns a code in the form "abcd-efgh-jkmn".
func newRecoveryCode() (string, error) {
	const groups, groupLen = 3, 4

	buf := make([]byte, groups*groupLen)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("generating recovery code: %w", err)
	}

	var b strings.Builder
	for i, v := range buf {
		if i > 0 && i%groupLen == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(recoveryAlphabet[int(v)%len(recoveryAlphabet)])
	}
	return b.String(), nil
}

// normaliseRecoveryCode makes matching forgiving about how it was typed.
func normaliseRecoveryCode(code string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
}

// DisableMFA turns off the second factor for a user's own account.
//
// Guarded by three things at once, because this is the one operation whose whole
// effect is to make an account easier to reach:
//
//   - the current password, so a borrowed unlocked session cannot do it;
//   - a valid second factor, so whoever asks still holds the thing being removed;
//   - the organisation role, because owner, admin and DPO reach personal data
//     across the whole organisation and are not permitted to run without one.
//
// Every session is revoked afterwards. Someone disabling a second factor is
// usually replacing a lost device, and any session opened with the old one
// should not survive that.
func (s *Service) DisableMFA(ctx context.Context, userID uuid.UUID, orgRole, currentPassword, code string) error {
	if domain.RequiresMFA(orgRole) {
		return fmt.Errorf("%w: vai trò %s bắt buộc dùng xác thực hai lớp",
			domain.ErrInvalidInput, orgRole)
	}

	user, err := s.store.FindUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if !user.MFAEnabled {
		return nil // Already off; saying so twice is not an error.
	}
	if _, err := password.Verify(currentPassword, user.PasswordHash); err != nil {
		return domain.ErrInvalidCredentials
	}
	if err := s.verifySecondFactor(ctx, userID, user.MFASecret, code); err != nil {
		return err
	}

	// The secret is cleared, not merely flagged off. Leaving it behind would mean
	// re-enabling silently restores a code somebody may have been shown years ago.
	if err := s.store.SetMFA(ctx, userID, nil, false); err != nil {
		return err
	}
	if err := s.store.DeleteRecoveryCodes(ctx, userID); err != nil {
		return err
	}
	if n, err := s.RevokeAll(ctx, userID, "mfa disabled"); err != nil {
		s.log.Warn("revoking sessions after disabling mfa", "error", err)
	} else {
		s.log.Info("mfa disabled", "user_id", userID, "sessions_revoked", n)
	}
	return nil
}
