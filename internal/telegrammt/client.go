package telegrammt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/SevastyanovYE/Sova/internal/config"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"rsc.io/qr"
)

type Client struct {
	cfg config.Config
}

type AuthStatus struct {
	Authorized    bool
	SessionExists bool
}

func New(cfg config.Config) *Client {
	return &Client{cfg: cfg}
}

func (c *Client) AuthStatus(ctx context.Context) (AuthStatus, error) {
	if err := c.validateRuntime(); err != nil {
		return AuthStatus{}, err
	}
	if err := ensureSessionDir(c.cfg.TelegramSessionPath); err != nil {
		return AuthStatus{}, err
	}
	client := c.newTelegramClient()
	status := AuthStatus{SessionExists: fileExists(c.cfg.TelegramSessionPath)}
	err := client.Run(ctx, func(runCtx context.Context) error {
		authStatus, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		status.Authorized = authStatus.Authorized
		return nil
	})
	if err != nil {
		return AuthStatus{}, err
	}
	return status, nil
}

func (c *Client) Login(ctx context.Context, in io.Reader, out io.Writer) error {
	if err := c.validateLogin(); err != nil {
		return err
	}
	if err := ensureSessionDir(c.cfg.TelegramSessionPath); err != nil {
		return err
	}
	client := c.newTelegramClient()
	reader := bufio.NewReader(in)
	_, _ = fmt.Fprintf(out, "starting dedicated Sova MTProto login for %s\n", maskPhone(c.cfg.TelegramPhone))
	return client.Run(ctx, func(runCtx context.Context) error {
		status, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if status.Authorized {
			_, _ = fmt.Fprintln(out, "session already authorized")
			return nil
		}
		_, _ = fmt.Fprintln(out, "requesting login code...")
		sentCodeClass, err := client.Auth().SendCode(runCtx, c.cfg.TelegramPhone, auth.SendCodeOptions{})
		if err != nil {
			return fmt.Errorf("send code: %w", err)
		}
		sentCode, ok, err := describeSentCode(out, sentCodeClass)
		if err != nil {
			return err
		}
		if !ok {
			if _, success := sentCodeClass.(*tg.AuthSentCodeSuccess); success {
				_, _ = fmt.Fprintln(out, "login successful")
				return nil
			}
			return fmt.Errorf("unexpected sent code type %T", sentCodeClass)
		}
		for {
			code, err := promptLine(out, reader, "code (or `resend`, `cancel`): ")
			if err != nil {
				return fmt.Errorf("read code: %w", err)
			}
			switch strings.ToLower(strings.TrimSpace(code)) {
			case "":
				_, _ = fmt.Fprintln(out, "empty code; paste the Telegram login code or type `resend`")
				continue
			case "cancel", "quit", "exit":
				return fmt.Errorf("login canceled")
			case "resend":
				sentCodeClass, err = client.Auth().ResendCode(runCtx, c.cfg.TelegramPhone, sentCode.PhoneCodeHash)
				if err != nil {
					return fmt.Errorf("resend code: %w", err)
				}
				sentCode, ok, err = describeSentCode(out, sentCodeClass)
				if err != nil {
					return err
				}
				if !ok {
					return fmt.Errorf("unexpected resent code type %T", sentCodeClass)
				}
				continue
			}
			if _, err := client.Auth().SignIn(runCtx, c.cfg.TelegramPhone, code, sentCode.PhoneCodeHash); err != nil {
				if tg.IsPhoneCodeInvalid(err) {
					_, _ = fmt.Fprintln(out, "Telegram says this code is invalid. Use the fresh code from Telegram, or type `resend`.")
					continue
				}
				if tg.IsPhoneCodeExpired(err) {
					_, _ = fmt.Fprintln(out, "Telegram says this code expired. Type `resend` to request a new one.")
					continue
				}
				if !errors.Is(err, auth.ErrPasswordAuthNeeded) {
					return fmt.Errorf("sign in: %w", err)
				}
				password := strings.TrimSpace(os.Getenv("SOVA_TELEGRAM_PASSWORD"))
				if password == "" {
					_, _ = fmt.Fprintln(out, "two-factor authentication is enabled")
					password, err = promptLine(out, reader, "password: ")
					if err != nil {
						return fmt.Errorf("read password: %w", err)
					}
				}
				if _, err := client.Auth().Password(runCtx, password); err != nil {
					return fmt.Errorf("sign in with password: %w", err)
				}
			}
			_, _ = fmt.Fprintln(out, "login successful")
			return nil
		}
	})
}

func (c *Client) LoginQR(ctx context.Context, in io.Reader, out io.Writer) error {
	if err := c.validateRuntime(); err != nil {
		return err
	}
	if err := ensureSessionDir(c.cfg.TelegramSessionPath); err != nil {
		return err
	}
	dispatcher := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(dispatcher)
	client := c.newTelegramClientWithHandler(dispatcher)
	reader := bufio.NewReader(in)
	_, _ = fmt.Fprintln(out, "starting dedicated Sova MTProto QR login")
	return client.Run(ctx, func(runCtx context.Context) error {
		status, err := client.Auth().Status(runCtx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if status.Authorized {
			_, _ = fmt.Fprintln(out, "session already authorized")
			return nil
		}
		_, _ = fmt.Fprintln(out, "scan this from an already logged-in Telegram app:")
		_, _ = fmt.Fprintln(out, "Telegram Settings -> Devices -> Link Desktop Device")
		showQR := func(ctx context.Context, token qrlogin.Token) error {
			_, _ = fmt.Fprintf(out, "\nlogin URL: %s\n", token.URL())
			if err := printTerminalQR(out, token.URL()); err != nil {
				_, _ = fmt.Fprintf(out, "QR render failed: %v\n", err)
			}
			_, _ = fmt.Fprintln(out, "waiting for scan and approval...")
			return nil
		}
		authorization, err := client.QR().Auth(runCtx, loggedIn, showQR)
		if err != nil {
			if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
				_, _ = fmt.Fprintln(out, "two-factor authentication is enabled")
				authorization, err = completePasswordAuth(runCtx, client, reader, out)
				if err != nil {
					return err
				}
			} else {
				return fmt.Errorf("qr login: %w", err)
			}
		}
		if authorization == nil {
			return fmt.Errorf("qr login completed without authorization")
		}
		user, ok := authorization.User.AsNotEmpty()
		if ok {
			_, _ = fmt.Fprintf(out, "login successful: id=%d username=%s\n", user.ID, user.Username)
			return nil
		}
		_, _ = fmt.Fprintln(out, "login successful")
		return nil
	})
}

func completePasswordAuth(ctx context.Context, client *telegram.Client, reader *bufio.Reader, out io.Writer) (*tg.AuthAuthorization, error) {
	password := strings.TrimSpace(os.Getenv("SOVA_TELEGRAM_PASSWORD"))
	var err error
	if password == "" {
		password, err = promptLine(out, reader, "password: ")
		if err != nil {
			return nil, fmt.Errorf("read password: %w", err)
		}
	}
	authorization, err := client.Auth().Password(ctx, password)
	if err != nil {
		return nil, fmt.Errorf("sign in with password: %w", err)
	}
	return authorization, nil
}

func (c *Client) validateRuntime() error {
	if c.cfg.TelegramAppID == 0 {
		return fmt.Errorf("SOVA_TELEGRAM_APP_ID is required")
	}
	if strings.TrimSpace(c.cfg.TelegramAppHash) == "" {
		return fmt.Errorf("SOVA_TELEGRAM_APP_HASH is required")
	}
	if strings.TrimSpace(c.cfg.TelegramSessionPath) == "" {
		return fmt.Errorf("SOVA_TELEGRAM_SESSION_PATH is required")
	}
	lower := strings.ToLower(c.cfg.TelegramSessionPath)
	if strings.Contains(lower, "telegram desktop") || strings.Contains(lower, "tdata") {
		return fmt.Errorf("Telegram Desktop sessions are forbidden")
	}
	return nil
}

func (c *Client) validateLogin() error {
	if err := c.validateRuntime(); err != nil {
		return err
	}
	if strings.TrimSpace(c.cfg.TelegramPhone) == "" {
		return fmt.Errorf("SOVA_TELEGRAM_PHONE is required")
	}
	return nil
}

func (c *Client) newTelegramClient() *telegram.Client {
	return c.newTelegramClientWithHandler(nil)
}

func (c *Client) newTelegramClientWithHandler(handler telegram.UpdateHandler) *telegram.Client {
	return telegram.NewClient(c.cfg.TelegramAppID, c.cfg.TelegramAppHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: c.cfg.TelegramSessionPath},
		Resolver:       dcs.Plain(dcs.PlainOptions{Dial: proxyAwareDialContext}),
		UpdateHandler:  handler,
	})
}

func ensureSessionDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func promptLine(out io.Writer, reader *bufio.Reader, label string) (string, error) {
	_, _ = fmt.Fprint(out, label)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func maskPhone(phone string) string {
	trimmed := strings.TrimSpace(phone)
	if len(trimmed) <= 4 {
		return trimmed
	}
	return trimmed[:2] + strings.Repeat("*", len(trimmed)-4) + trimmed[len(trimmed)-2:]
}

func describeSentCode(out io.Writer, sentCodeClass tg.AuthSentCodeClass) (*tg.AuthSentCode, bool, error) {
	sentCode, ok := sentCodeClass.(*tg.AuthSentCode)
	if !ok {
		return nil, false, nil
	}
	_, _ = fmt.Fprintf(out, "code delivery: %s\n", describeSentCodeType(sentCode.Type))
	if nextType, hasNext := sentCode.GetNextType(); hasNext {
		timeout, _ := sentCode.GetTimeout()
		if timeout > 0 {
			_, _ = fmt.Fprintf(out, "next delivery option after %d seconds: %s; type `resend` then\n", timeout, describeNextCodeType(nextType))
		} else {
			_, _ = fmt.Fprintf(out, "next delivery option: %s; type `resend` if the code did not arrive\n", describeNextCodeType(nextType))
		}
	} else {
		_, _ = fmt.Fprintln(out, "if the code does not arrive, check Telegram apps where this account is already logged in")
	}
	return sentCode, true, nil
}

func describeSentCodeType(codeType tg.AuthSentCodeTypeClass) string {
	switch typed := codeType.(type) {
	case *tg.AuthSentCodeTypeApp:
		return fmt.Sprintf("Telegram app login code (%d digits)", typed.Length)
	case *tg.AuthSentCodeTypeSMS:
		return fmt.Sprintf("SMS login code (%d digits)", typed.Length)
	case *tg.AuthSentCodeTypeCall:
		return fmt.Sprintf("phone call login code (%d digits)", typed.Length)
	case *tg.AuthSentCodeTypeFlashCall:
		return "flash call login code; enter the phone number matching Telegram's pattern"
	case *tg.AuthSentCodeTypeMissedCall:
		return fmt.Sprintf("missed call login code (%d digits); use the last digits of the incoming caller number", typed.Length)
	case *tg.AuthSentCodeTypeEmailCode:
		return "email login code"
	case *tg.AuthSentCodeTypeFragmentSMS:
		return "Fragment SMS login code"
	case *tg.AuthSentCodeTypeFirebaseSMS:
		return "Firebase SMS login code"
	case *tg.AuthSentCodeTypeSMSWord:
		return "SMS login word"
	case *tg.AuthSentCodeTypeSMSPhrase:
		return "SMS login phrase"
	default:
		return fmt.Sprintf("unknown Telegram delivery type %T", codeType)
	}
}

func describeNextCodeType(codeType tg.AuthCodeTypeClass) string {
	switch codeType.(type) {
	case *tg.AuthCodeTypeSMS:
		return "SMS"
	case *tg.AuthCodeTypeCall:
		return "phone call"
	case *tg.AuthCodeTypeFlashCall:
		return "flash call"
	case *tg.AuthCodeTypeMissedCall:
		return "missed call"
	case *tg.AuthCodeTypeFragmentSMS:
		return "Fragment SMS"
	default:
		return fmt.Sprintf("unknown Telegram delivery type %T", codeType)
	}
}

func printTerminalQR(out io.Writer, text string) error {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return err
	}
	const border = 2
	for y := -border; y < code.Size+border; y += 2 {
		for x := -border; x < code.Size+border; x++ {
			top := code.Black(x, y)
			bottom := code.Black(x, y+1)
			switch {
			case top && bottom:
				_, _ = fmt.Fprint(out, "█")
			case top && !bottom:
				_, _ = fmt.Fprint(out, "▀")
			case !top && bottom:
				_, _ = fmt.Fprint(out, "▄")
			default:
				_, _ = fmt.Fprint(out, " ")
			}
		}
		_, _ = fmt.Fprintln(out)
	}
	return nil
}
