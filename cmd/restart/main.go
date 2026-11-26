// Command restart restarts the server's systemd service over SSH, using the
// same settings as cmd/upload (task loads them from .env):
//
//	DEPLOY_HOST      server host name or IP
//	DEPLOY_PORT      SSH port (default 22)
//	DEPLOY_USER      SSH user
//	DEPLOY_PASSWORD  SSH password, also used for sudo
//	DEPLOY_SERVICE   systemd unit to restart (default ghostman)
//
// As root it runs `systemctl restart` directly. As anyone else it runs it
// through `sudo -S`, which reads DEPLOY_PASSWORD from stdin: nothing prompts,
// and the password never appears on a command line. Afterwards it waits a
// moment and checks that the unit is still active, so a binary that crashes
// on start is reported instead of looking like a successful restart.
package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"golang.org/x/crypto/ssh"

	"github.com/komiljonov/ghostman/internal/deploy"
)

const (
	defaultService = "ghostman"

	// settle is how long the unit must stay up after the restart before it
	// counts as running; a binary that fails on start usually exits within it.
	settle = 2 * time.Second

	// statusLines is how much of `systemctl status` to show when it fails.
	statusLines = 20
)

// serviceName is what systemd accepts in a unit name, which also keeps the
// name inert in the remote shell command.
var serviceName = regexp.MustCompile(`^[A-Za-z0-9@._:-]+$`)

type config struct {
	deploy.SSH
	Service string `env:"DEPLOY_SERVICE"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "restart failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	started := time.Now()

	var cfg config
	if err := env.Parse(&cfg); err != nil {
		return fmt.Errorf("reading deploy settings (set them in .env, see .env.example): %w", err)
	}

	// Unset and empty both mean the default.
	service := strings.TrimSpace(cfg.Service)
	if service == "" {
		service = defaultService
	}
	if !serviceName.MatchString(service) {
		return fmt.Errorf("DEPLOY_SERVICE %q is not a valid systemd unit name", service)
	}
	deploy.LogStep("service", "%s", service)

	client, err := deploy.Connect(cfg.SSH)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := restart(client, service, cfg.Password); err != nil {
		return err
	}

	return checkActive(client, service, started)
}

// restart runs systemctl restart, through sudo unless the login is root.
func restart(client *ssh.Client, service, password string) error {
	uid, err := deploy.Output(client, "id -u")
	if err != nil {
		return fmt.Errorf("checking the remote user: %w", err)
	}

	command := "systemctl restart " + deploy.ShellQuote(service)
	stdin := ""
	how := "as root"
	if uid != "0" {
		// -S reads the password from stdin, -p '' drops the prompt text, and
		// -k ignores any cached credential so the password is always checked
		// the same way. With NOPASSWD sudo, stdin is simply never read.
		command = "sudo -S -k -p '' " + command
		stdin = password + "\n"
		how = "via sudo"
	}

	deploy.LogStep("restarting", "systemctl restart %s (%s)", service, how)
	restartStart := time.Now()

	output, err := deploy.Exec(client, command, stdin)
	if err != nil {
		return restartError(err, output, service)
	}

	deploy.LogStep("restarted", "in %s", deploy.RoundDuration(time.Since(restartStart)))
	return nil
}

// restartError explains the failures worth explaining.
func restartError(err error, output, service string) error {
	switch {
	case strings.Contains(output, "incorrect password") || strings.Contains(output, "Sorry, try again"):
		return errors.New("sudo rejected DEPLOY_PASSWORD: the user's sudo password differs from the SSH password")
	case strings.Contains(output, "not in the sudoers file") || strings.Contains(output, "is not allowed to run sudo"):
		return errors.New("DEPLOY_USER may not use sudo on the server; it needs sudo rights for systemctl, or log in as root")
	case strings.Contains(output, "not found") && strings.Contains(output, service):
		return fmt.Errorf("the server has no systemd unit %q; set DEPLOY_SERVICE to the right name: %s", service, output)
	case output != "":
		return fmt.Errorf("systemctl restart %s: %w: %s", service, err, output)
	default:
		return fmt.Errorf("systemctl restart %s: %w", service, err)
	}
}

// checkActive waits for the unit to settle and reports whether it stayed up.
// Reading unit state needs no privileges, so this never uses sudo.
func checkActive(client *ssh.Client, service string, started time.Time) error {
	deploy.LogStep("checking", "waiting %s for %s to settle ...", settle, service)
	time.Sleep(settle)

	quoted := deploy.ShellQuote(service)
	state, _ := deploy.Exec(client, "systemctl is-active "+quoted, "")
	if state != "active" {
		status, _ := deploy.Exec(client,
			fmt.Sprintf("systemctl status %s --no-pager --full --lines=%d", quoted, statusLines), "")
		if status != "" {
			fmt.Fprintln(os.Stderr, status)
		}
		if state == "" {
			state = "unknown"
		}
		return fmt.Errorf("%s is %s after the restart (status above)", service, state)
	}

	details, _ := deploy.Exec(client,
		"systemctl show "+quoted+" --property=MainPID --property=ActiveEnterTimestamp", "")
	// One property per line; the timestamp itself contains spaces.
	deploy.LogStep("running", "%s", strings.Join(strings.Split(details, "\n"), ", "))
	deploy.LogStep("done", "in %s", deploy.RoundDuration(time.Since(started)))
	return nil
}
