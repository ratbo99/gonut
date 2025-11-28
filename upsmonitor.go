package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/getlantern/systray"
	"github.com/go-toast/toast"
	"golang.org/x/sys/windows/registry"

	"embed"
	"syscall"
	"unsafe"

	"github.com/natefinch/npipe"
)

// ================= EMBED =================

//go:embed assets/ok.ico
var iconOK []byte

//go:embed assets/error.ico
var iconError []byte

var _ embed.FS

// ================= CONFIG =================

type Config struct {
	Host                string `json:"host"`
	Port                int    `json:"port"`
	User                string `json:"user"`
	Password            string `json:"password"`
	UPSName             string `json:"upsName"`
	PollInterval        int    `json:"pollInterval"`
	ShutdownDelay       int    `json:"shutdownDelay"`
	EnableNotifications bool   `json:"enableNotifications"`
	Autostart           bool   `json:"autostart"`
}

var defaultConfig = Config{
	Host:                "192.168.1.50",
	Port:                3493,
	User:                "",
	Password:            "",
	UPSName:             "ups",
	PollInterval:        10000,
	ShutdownDelay:       120000,
	EnableNotifications: true,
	Autostart:           true,
}

func configPath() string {

	// when compiled: next to exe
	exe, err := os.Executable()
	if err == nil {
		exe = filepath.Clean(exe)
	}

	// when running via go run => use working dir
	if strings.Contains(exe, "go-build") || strings.Contains(exe, "Temp") || strings.Contains(exe, "Cache") {
		dir, _ := os.Getwd()
		return filepath.Join(dir, "config.json")
	}

	return filepath.Join(filepath.Dir(exe), "config.json")
}

func saveConfig(cfg Config) {
	raw, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(configPath(), raw, 0644)
}

func loadConfig() Config {

	path := configPath()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		raw, _ := json.MarshalIndent(defaultConfig, "", "  ")
		_ = os.WriteFile(path, raw, 0644)
		fmt.Println("⚠️  config.json wurde erstellt. Bitte ausfüllen und neu starten.")
		os.Exit(0)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("❌ Fehler config.json:", err)
		os.Exit(1)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fmt.Println("❌ JSON Fehler:", err)
		os.Exit(1)
	}
	return cfg
}

// ================= AUTOSTART =================

func applyAutostart(cfg Config) {

	if runtime.GOOS != "windows" {
		return
	}

	exe, err := os.Executable()
	if err != nil {
		return
	}

	key, _, err := registry.CreateKey(
		registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		registry.SET_VALUE,
	)
	if err != nil {
		return
	}
	defer key.Close()

	const name = "UPSMonitor"

	if cfg.Autostart {
		_ = key.SetStringValue(name, exe)
	} else {
		_ = key.DeleteValue(name)
	}
}

// ================= NUT CLIENT =================

type NutClient struct {
	cfg    Config
	conn   net.Conn
	reader *bufio.Reader
}

func NewNutClient(cfg Config) *NutClient {
	return &NutClient{cfg: cfg}
}

func (n *NutClient) Connect() error {
	addr := fmt.Sprintf("%s:%d", n.cfg.Host, n.cfg.Port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	n.conn = conn
	n.reader = bufio.NewReader(conn)
	return nil
}

func (n *NutClient) Send(cmd string) (string, error) {

	_, err := n.conn.Write([]byte(cmd + "\n"))
	if err != nil {
		return "", err
	}

	_ = n.conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	resp, err := n.reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(resp), nil
}

func (n *NutClient) Login() error {

	if n.cfg.User == "" {
		return nil
	}

	r, err := n.Send("USERNAME " + n.cfg.User)
	if err != nil || !strings.HasPrefix(r, "OK") {
		return fmt.Errorf("user auth failed")
	}

	r, err = n.Send("PASSWORD " + n.cfg.Password)
	if err != nil || !strings.HasPrefix(r, "OK") {
		return fmt.Errorf("pass auth failed")
	}

	return nil
}

func (n *NutClient) GetVar(v string) string {
	resp, err := n.Send(fmt.Sprintf("GET VAR %s %s", n.cfg.UPSName, v))
	if err != nil {
		return "?"
	}
	if s := strings.Split(resp, "\""); len(s) >= 2 {
		return s[1]
	}
	return "?"
}

// ================= NOTIFY =================

func notify(cfg Config, title, msg string) {

	if !cfg.EnableNotifications {
		return
	}

	if runtime.GOOS == "windows" {
		n := toast.Notification{
			AppID:   "UPS Monitor",
			Title:   title,
			Message: msg,
		}
		n.Push()
	}
}

// ================= SHUTDOWN =================

var shutdownTimer *time.Timer

func startShutdown(cfg Config) {

	if shutdownTimer != nil {
		return
	}

	notify(cfg, "Stromausfall", "Shutdown Timer gestartet")

	shutdownTimer = time.AfterFunc(
		time.Duration(cfg.ShutdownDelay)*time.Millisecond,
		func() {

			notify(cfg, "Shutdown", "PC wird jetzt heruntergefahren")

			if runtime.GOOS == "windows" {
				exec.Command("shutdown", "/s", "/t", "0", "/f").Run()
			} else {
				exec.Command("shutdown", "now").Run()
			}
		},
	)
}

func cancelShutdown(cfg Config) {
	if shutdownTimer != nil {
		shutdownTimer.Stop()
		shutdownTimer = nil
		notify(cfg, "Stromversorgung", "Shutdown abgebrochen")
	}
}

// ================= TRAY =================

func setOK()  { systray.SetIcon(iconOK) }
func setERR() { systray.SetIcon(iconError) }

var pipe net.Listener

func ensureSingleInstance() {

	var err error

	pipe, err = npipe.Listen(`\\.\pipe\UPSMonitorSingleton`)

	if err != nil {
		showMessageBox("UPS Monitor", "UPS Monitor läuft bereits.")
		os.Exit(0)
	}

	// Pipe offen halten, sonst wird sie freigegeben
	go func() {
		for {
			conn, err := pipe.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
}

func showMessageBox(title, text string) {
	user32 := syscall.NewLazyDLL("user32.dll")
	proc := user32.NewProc("MessageBoxW")

	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)

	proc.Call(0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		0)
}

// ================= MAIN =================

func main() {
	ensureSingleInstance()
	systray.Run(onReady, onExit)
}

func onReady() {

	cfg := loadConfig()
	applyAutostart(cfg)

	setOK()
	systray.SetTooltip("UPS Monitor gestartet")

	// ---------- MENU ----------
	itemAutostart := systray.AddMenuItemCheckbox("Autostart", "Mit Windows starten", cfg.Autostart)
	itemNotify := systray.AddMenuItemCheckbox("Benachrichtigungen", "Windows Toast", cfg.EnableNotifications)
	quit := systray.AddMenuItem("Beenden", "Programm schließen")

	// ---------- MENU HANDLER ----------
	go func() {

		for {

			select {

			case <-itemAutostart.ClickedCh:
				cfg.Autostart = !cfg.Autostart
				if cfg.Autostart {
					itemAutostart.Check()
				} else {
					itemAutostart.Uncheck()
				}
				applyAutostart(cfg)
				saveConfig(cfg)

			case <-itemNotify.ClickedCh:
				cfg.EnableNotifications = !cfg.EnableNotifications
				if cfg.EnableNotifications {
					itemNotify.Check()
				} else {
					itemNotify.Uncheck()
				}
				saveConfig(cfg)

			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()

	// ---------- NUT ----------
	client := NewNutClient(cfg)
	if err := client.Connect(); err != nil {
		setERR()
		return
	}
	client.Login()

	// ---------- LOOP ----------
	go func() {

		for {

			status := client.GetVar("ups.status")
			charge := client.GetVar("battery.charge")

			if status == "?" {
				setERR()
				systray.SetTooltip("Verbindung fehlgeschlagen!\nPrüfe verbindung, oder Logindaten")
				continue
			}

			mode := status

			if strings.Contains(status, "OL") {
				mode = "Online"
				setOK()
				cancelShutdown(cfg)

			} else if strings.Contains(status, "OB") {
				mode = "Batterie"
				setERR()
				startShutdown(cfg)
			}

			systray.SetTooltip(fmt.Sprintf(
				"USV: %s\nStatus: %s\nLadung: %s%%",
				cfg.UPSName,
				mode,
				charge,
			))

			time.Sleep(time.Duration(cfg.PollInterval) * time.Millisecond)
		}
	}()
}
func onExit() {
	if pipe != nil {
		pipe.Close()
	}
}
