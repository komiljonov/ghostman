// Package deploy holds what the deployment commands (cmd/upload, cmd/restart)
// share: the SSH settings from the environment, a connection checked against
// ~/.ssh/known_hosts, running commands on the server, and the aligned step
// output.
//
// It is Go rather than calls to the ssh binary because OpenSSH cannot take a
// password non-interactively, and sshpass does not exist on Windows; this runs
// the same everywhere.
package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const dialTimeout = 15 * time.Second

// SSH is the server login, read from the environment (task loads it from
// .env). Commands embed it in their own config.
type SSH struct {
	Host string `env:"DEPLOY_HOST,required,notEmpty"`
	Port int    `env:"DEPLOY_PORT" envDefault:"22"`
	User string `env:"DEPLOY_USER,required,notEmpty"`
	// unset removes the password from this process's environment once read.
	Password string `env:"DEPLOY_PASSWORD,required,notEmpty,unset"`
}

// Connect opens an SSH session with password auth, verifying the server
// against ~/.ssh/known_hosts, and logs the connecting/connected steps.
func Connect(s SSH) (*ssh.Client, error) {
	hostKeys, err := knownHostsCallback()
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	LogStep("connecting", "%s@%s ...", s.User, addr)

	// Wrapped to report which key was accepted.
	var acceptedKey string
	dialStart := time.Now()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: s.User,
		Auth: []ssh.AuthMethod{ssh.Password(s.Password)},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			if keyErr := hostKeys(hostname, remote, key); keyErr != nil {
				return keyErr
			}
			acceptedKey = strings.ToUpper(strings.TrimPrefix(key.Type(), "ssh-")) + " " + ssh.FingerprintSHA256(key)
			return nil
		},
		Timeout: dialTimeout,
	})
	if err != nil {
		return nil, dialError(err, s, addr)
	}

	LogStep("connected", "in %s, host key %s (known_hosts)", RoundDuration(time.Since(dialStart)), acceptedKey)
	return client, nil
}

// dialError explains the connection failures worth explaining.
func dialError(err error, s SSH, addr string) error {
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			return fmt.Errorf("%s is not in ~/.ssh/known_hosts yet: connect once with `ssh -p %d %s@%s` and accept its key, then retry",
				s.Host, s.Port, s.User, s.Host)
		}
		return fmt.Errorf("the host key of %s has CHANGED since it was added to ~/.ssh/known_hosts. "+
			"This can mean the server was reinstalled, or that someone is intercepting the connection. "+
			"Refusing to connect; check with the server's owner before removing the old key", s.Host)
	}

	if strings.Contains(err.Error(), "unable to authenticate") {
		return fmt.Errorf("connecting to %s: the server rejected DEPLOY_USER / DEPLOY_PASSWORD", addr)
	}

	return fmt.Errorf("connecting to %s: %w", addr, err)
}

// knownHostsCallback verifies the server against the user's known_hosts, the
// same file the ssh command uses.
func knownHostsCallback() (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("finding home directory for known_hosts: %w", err)
	}

	callback, err := knownhosts.New(filepath.Join(home, ".ssh", "known_hosts"))
	if err != nil {
		return nil, fmt.Errorf("reading ~/.ssh/known_hosts (connect once with ssh to create it): %w", err)
	}

	return callback, nil
}

// Run runs one shell command on the server.
func Run(client *ssh.Client, command string) error {
	_, err := Output(client, command)
	return err
}

// Output runs one shell command on the server and returns its output.
func Output(client *ssh.Client, command string) (string, error) {
	text, err := Exec(client, command, "")
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

// Exec runs one shell command on the server with stdin, and returns its
// combined output, trimmed, even when the command fails (a non-zero exit is
// the error). stdin carries secrets such as a sudo password, so they never
// appear on a command line.
func Exec(client *ssh.Client, command, stdin string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening ssh session: %w", err)
	}
	defer session.Close()

	if stdin != "" {
		session.Stdin = strings.NewReader(stdin)
	}

	output, err := session.CombinedOutput(command)
	return strings.TrimSpace(string(output)), err
}

// ShellQuote quotes s for a POSIX shell on the server.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// StderrSuffix formats captured stderr for an error message, or "" if empty.
func StderrSuffix(stderr *bytes.Buffer) string {
	if text := strings.TrimSpace(stderr.String()); text != "" {
		return " (" + text + ")"
	}
	return ""
}

// Output is plain ASCII on purpose: a Windows console on a legacy code page
// garbles box-drawing and check-mark characters.

// LabelWidth aligns the step labels into a column.
const LabelWidth = 10

// LogStep prints one aligned step line, e.g. "connected  in 0.4s, ...".
func LogStep(label, format string, args ...any) {
	fmt.Printf("%-*s %s\n", LabelWidth, label, fmt.Sprintf(format, args...))
}

// RoundDuration keeps durations readable: milliseconds below a second, tenths
// of a second below a minute, whole seconds above.
func RoundDuration(d time.Duration) time.Duration {
	switch {
	case d < time.Second:
		return d.Round(time.Millisecond)
	case d < time.Minute:
		return d.Round(100 * time.Millisecond)
	default:
		return d.Round(time.Second)
	}
}
