// Command upload copies the built server binary to the deployment server over
// SCP, using the credentials and target folder from the environment (task
// loads them from .env):
//
//	DEPLOY_HOST      server host name or IP
//	DEPLOY_PORT      SSH port (default 22)
//	DEPLOY_USER      SSH user
//	DEPLOY_PASSWORD  SSH password
//	DEPLOY_DIR       folder on the server to upload into
//
// It is a Go program rather than a call to the scp binary because OpenSSH
// cannot take a password non-interactively, and sshpass does not exist on
// Windows; this runs the same everywhere. It does not build anything: run
// `task build-linux` first.
//
// The server's host key is checked against ~/.ssh/known_hosts. The upload
// lands under a temporary name, is checked by sha256 on the server, and is
// then renamed into place. Overwriting a running binary in place either fails
// ("text file busy", on most kernels) or, where the kernel allows it, rewrites
// code under the running process; a rename does neither, and a damaged upload
// never replaces the live binary.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	// localBinary is what `task build-linux` produces.
	localBinary = "dist/ghostman-server"

	// remoteName is the file name on the server.
	remoteName = "ghostman-server"

	// tempName is what the upload is written to before being renamed.
	tempName = ".ghostman-server.upload"

	dialTimeout = 15 * time.Second
)

type config struct {
	Host string `env:"DEPLOY_HOST,required,notEmpty"`
	Port int    `env:"DEPLOY_PORT" envDefault:"22"`
	User string `env:"DEPLOY_USER,required,notEmpty"`
	// unset removes the password from this process's environment once read.
	Password string `env:"DEPLOY_PASSWORD,required,notEmpty,unset"`
	Dir      string `env:"DEPLOY_DIR,required,notEmpty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "upload failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	started := time.Now()

	var cfg config
	if err := env.Parse(&cfg); err != nil {
		return fmt.Errorf("reading deploy settings (set them in .env, see .env.example): %w", err)
	}

	file, err := os.Open(localBinary)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s not found: run `task build-linux` first", localBinary)
		}
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	// Hashed up front: shown now, and compared with the server's copy before
	// it replaces the live binary.
	localSum, err := sha256File(file)
	if err != nil {
		return fmt.Errorf("hashing %s: %w", localBinary, err)
	}
	logStep("binary", "%s, %s, sha256 %s", localBinary, formatBytes(info.Size()), localSum[:12])

	hostKeys, err := knownHostsCallback()
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	logStep("connecting", "%s@%s ...", cfg.User, addr)

	// Wrapped to report which key was accepted.
	var acceptedKey string
	dialStart := time.Now()
	client, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User: cfg.User,
		Auth: []ssh.AuthMethod{ssh.Password(cfg.Password)},
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
		return dialError(err, cfg, addr)
	}
	defer client.Close()
	logStep("connected", "in %s, host key %s (known_hosts)", roundDuration(time.Since(dialStart)), acceptedKey)

	tempPath := path.Join(cfg.Dir, tempName)
	target := path.Join(cfg.Dir, remoteName)

	logStep("uploading", "%s", tempPath)
	progress := newProgress(info.Size())
	uploadStart := time.Now()
	if err := scpSend(client, cfg.Dir, tempName, info.Size(), progress.reader(file)); err != nil {
		progress.abort()
		return err
	}
	progress.finish()
	elapsed := time.Since(uploadStart)
	logStep("uploaded", "%s in %s (%s/s)",
		formatBytes(info.Size()), roundDuration(elapsed), formatBytes(int64(float64(info.Size())/elapsed.Seconds())))

	if err := verifyRemote(client, tempPath, localSum); err != nil {
		return err
	}

	// Rename into place: atomic, and safe while the old binary is running,
	// which keeps the file it started from until it exits.
	logStep("installing", "%s", target)
	if err := runRemote(client, "mv -f "+shellQuote(tempPath)+" "+shellQuote(target)); err != nil {
		return fmt.Errorf("moving the upload into place: %w", err)
	}

	logStep("done", "in %s; restart the service on the server to run the new binary", roundDuration(time.Since(started)))
	return nil
}

// dialError explains the connection failures worth explaining.
func dialError(err error, cfg config, addr string) error {
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			return fmt.Errorf("%s is not in ~/.ssh/known_hosts yet: connect once with `ssh -p %d %s@%s` and accept its key, then retry",
				cfg.Host, cfg.Port, cfg.User, cfg.Host)
		}
		return fmt.Errorf("the host key of %s has CHANGED since it was added to ~/.ssh/known_hosts. "+
			"This can mean the server was reinstalled, or that someone is intercepting the connection. "+
			"Refusing to upload; check with the server's owner before removing the old key", cfg.Host)
	}

	if strings.Contains(err.Error(), "unable to authenticate") {
		return fmt.Errorf("connecting to %s: the server rejected DEPLOY_USER / DEPLOY_PASSWORD", addr)
	}

	return fmt.Errorf("connecting to %s: %w", addr, err)
}

// verifyRemote compares the uploaded file's sha256 with the local one, so a
// damaged upload never replaces the running binary. A server without
// sha256sum is reported and the check skipped.
func verifyRemote(client *ssh.Client, remotePath, want string) error {
	logStep("verifying", "sha256 on the server ...")

	output, err := remoteOutput(client, "sha256sum "+shellQuote(remotePath))
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			logStep("verifying", "skipped: sha256sum is not available on the server")
			return nil
		}
		return fmt.Errorf("checking the upload on the server: %w", err)
	}

	fields := strings.Fields(output)
	if len(fields) == 0 {
		return fmt.Errorf("checking the upload on the server: unexpected sha256sum output %q", output)
	}

	if fields[0] != want {
		// Leave the live binary alone and clean up the bad copy.
		_ = runRemote(client, "rm -f "+shellQuote(remotePath))
		return fmt.Errorf("the uploaded file is damaged (sha256 %s, want %s); the live binary was not touched", fields[0][:12], want[:12])
	}

	logStep("verified", "sha256 matches")
	return nil
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

// sha256File hashes f and rewinds it for the upload.
func sha256File(f *os.File) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// scpSend uploads one file into dir under name, speaking the sink side of the
// SCP protocol that `scp -t` runs on the server.
func scpSend(client *ssh.Client, dir, name string, size int64, content io.Reader) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("opening ssh session: %w", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Start("scp -t " + shellQuote(dir)); err != nil {
		return fmt.Errorf("starting scp on the server: %w", err)
	}

	ack := func(step string) error {
		if err := readAck(stdout); err != nil {
			return fmt.Errorf("%s: %w%s", step, err, stderrSuffix(&stderr))
		}
		return nil
	}

	if err := ack("waiting for the server"); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdin, "C0755 %d %s\n", size, name); err != nil {
		return err
	}
	if err := ack("sending file header"); err != nil {
		return err
	}

	if _, err := io.Copy(stdin, content); err != nil {
		return fmt.Errorf("sending file: %w", err)
	}
	if _, err := stdin.Write([]byte{0}); err != nil {
		return err
	}
	if err := ack("finishing transfer"); err != nil {
		return err
	}

	if err := stdin.Close(); err != nil {
		return err
	}
	if err := session.Wait(); err != nil {
		return fmt.Errorf("scp on the server: %w%s", err, stderrSuffix(&stderr))
	}

	return nil
}

// readAck reads one SCP status byte: 0 is OK, 1 and 2 carry a message.
func readAck(r io.Reader) error {
	status := make([]byte, 1)
	if _, err := io.ReadFull(r, status); err != nil {
		return fmt.Errorf("no reply: %w", err)
	}
	if status[0] == 0 {
		return nil
	}

	var message []byte
	buf := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, buf); err != nil || buf[0] == '\n' {
			break
		}
		message = append(message, buf[0])
	}

	return errors.New(strings.TrimSpace(string(message)))
}

// runRemote runs one shell command on the server.
func runRemote(client *ssh.Client, command string) error {
	_, err := remoteOutput(client, command)
	return err
}

// remoteOutput runs one shell command on the server and returns its output.
func remoteOutput(client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("opening ssh session: %w", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput(command)
	text := strings.TrimSpace(string(output))
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

// shellQuote quotes s for a POSIX shell on the server.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func stderrSuffix(stderr *bytes.Buffer) string {
	if text := strings.TrimSpace(stderr.String()); text != "" {
		return " (" + text + ")"
	}
	return ""
}
