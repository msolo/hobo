package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	doc = `hobo - manage local virtual machines

Configuration is read from -config-file, ./.hobo, or ~/.hobo - whichever occurs first.

start - start a vm
stop - stop a vm
suspend - suspend a vm

ip-addr - return the current ip address for a vm
ssh - ssh into a vm
ssh-config - generate an ssh config clause for a vm

ls - show all running vms
rm - destroy a vm and permanently remove all data files

fetch - pull down a boxcar archive

make-boxcar <boxcar name>.vmwarevw - create a new boxcar archive
`

	bootstrapInsecurePrivateKey = `-----BEGIN RSA PRIVATE KEY-----
MIICXAIBAAKBgQDEtXNb2AXyVTTpJnM58d3ouSSALYftJi7x1m5cCbcE+hpWeQdY
LPNH1kEoR5wFADpi2lr+38ewS1mvwu1+M2NCABZc60svjZTnQLyGpNr/+IwM0SSm
/RBlJenuNnE645Sg2CkuW4q2izzthGDAUt353ZY1DRANXsqkM59XDORSbQIDAQAB
AoGATaCp2LnkhuC3U7c3y8s2XqwJyoetV5o09n0/6hRvZIhqsmtqyZJbo6La7dFs
sdCIOhCfzmtze5AQ4brUTHRtG6HZQfFhH+uMMOVquWh8cuz/6mz7mp987rJzYhdu
CceIc6TiiK802bw/rJoC9NkE3FSdXlwH5GMQQqvaqwT1XWUCQQD2QgvZ5Xct4P7s
glZkSC2t89Uyd6JfUcE9sUSVUVEcaXBL+RJIt9yjsEZwa7bImvmjSl6XeYJa+5WN
JgBwopUrAkEAzH2ZgeQWxvsK1BjxGVQAsLJ+ObkCP+SwAkrt9ZNlC8B23KO++PvX
Ng8LaQwkLTr61BRRgFNClFjxfR2P/BQaxwJAZuSLvRyConnLKhj/beE2rOMfpnmU
L42iV1uVE2qpoFxx3lyQhi/EkeRaWii3c7RFMDQnt9S+YbOS9in1rxpPhwJAJVIz
BxLS2WQN+OHIdv/u1FDvWqeacoDRYsm8HlrVUUzCJMi53QVRpOsgAP8XRy4Bg11l
9o67kwmcoWIY2j/tFwJBANpifWelhjrFYGGfm7iuGVgpFZbyGscsGgFvBXxjTzaC
oRyJjWctWDFI5W8qJzOpmgD+Ifv/oNaQVoPpCQ9GfXI=
-----END RSA PRIVATE KEY-----
`

	bootstrapInsecurePublicKey = `ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQDEtXNb2AXyVTTpJnM58d3ouSSALYftJi7x1m5cCbcE+hpWeQdYLPNH1kEoR5wFADpi2lr+38ewS1mvwu1+M2NCABZc60svjZTnQLyGpNr/+IwM0SSm/RBlJenuNnE645Sg2CkuW4q2izzthGDAUt353ZY1DRANXsqkM59XDORSbQ== hobo-boostrap-insecure
`
)

// Normally you will have an arena directory that contains all the hobo-related files per-user.
// ~/.hobo.d/ - the "arena".
// ~/.hobo.d/cache/boxcars/ - cached files, mostly boxcar archive files.
// ~/.hobo.d/vms/ - the actual vm data - the important stuff.
// ~/.hobo - user config overrides.
// ./.hobo - local config overrides.
type appConfig struct {
	VmrunBinaryPath        string
	VdiskManagerBinaryPath string
	HoboDir                string
}

func (ac *appConfig) vmsDir() string {
	return path.Join(ac.HoboDir, "vms")
}

func (ac *appConfig) boxcarsDir() string {
	return path.Join(ac.HoboDir, "cache/boxcars")
}

type boxcar struct {
	Name              string
	Url               string
	Version           string
	Sha256            string
	BootstrapCmdLines []string
}

func newBoxcarFromFile(ac appConfig, fname string) (*boxcar, error) {
	data, err := ioutil.ReadFile(fname)
	if err != nil {
		return nil, err
	}
	bxc := &boxcar{}
	if err = json.Unmarshal(data, bxc); err != nil {
		return nil, err
	}
	return bxc, nil
}

func (bxc *boxcar) bootstrapBashScript() string {
	cmdLines := []string{
		"export HOBO_HOST_USER=" + os.Getenv("LOGNAME"),
		"export HOBO_CMD=bootstrap",
		"cd /tmp",
	}
	cmdLines = append(cmdLines, bxc.BootstrapCmdLines...)
	cmdLines = append(cmdLines, "echo hobo-bootstrap-ok")
	return strings.Join(cmdLines, "\n")
}

type localConfig struct {
	AppConfig appConfig
	Boxcar    boxcar
	Name      string
}

func newLocalConfigFromFile(fname string) (*localConfig, error) {
	lc := &localConfig{
		AppConfig: appConfig{
			VmrunBinaryPath:        "/Applications/VMware Fusion.app/Contents/Library/vmrun",
			VdiskManagerBinaryPath: "/Applications/VMware Fusion.app/Contents/Library/vmware-vdiskmanager",
			HoboDir:                "$HOME/.hobo.d",
		},
	}
	if fname == "" {
		return lc, nil
	}
	data, err := ioutil.ReadFile(fname)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, lc); err != nil {
		return nil, err
	}
	lc.AppConfig.HoboDir = os.ExpandEnv(lc.AppConfig.HoboDir)
	return lc, nil
}

type vmConfig struct {
	TimeBootstrapped time.Time
	IpAddr           string

	appConfig  appConfig
	boxcar     boxcar
	vmPath     string
	vmxFile    string
	configFile string
	sshId      string
	sshIdPub   string
}

func newVmConfig(ac appConfig, vmPath string) (*vmConfig, error) {
	configFile := path.Join(vmPath, "hobo/config.json")
	vmName := path.Base(vmPath)
	vmxFile := path.Join(vmPath, vmName[:len(vmName)-len(path.Ext(vmName))]+".vmx")
	sshId := path.Join(vmPath, "hobo-insecure")
	sshIdPub := sshId + ".pub"
	cfg := &vmConfig{
		appConfig:  ac,
		vmPath:     vmPath,
		vmxFile:    vmxFile,
		configFile: configFile,
		sshId:      sshId,
		sshIdPub:   sshIdPub,
	}
	return cfg, nil
}

func newVmConfigForName(ac appConfig, name string) (*vmConfig, error) {
	vmPath := path.Join(ac.vmsDir(), name+".vmwarevm")
	return newVmConfig(ac, vmPath)
}

type instance struct {
	name     string
	vmConfig vmConfig
}

func newInstanceForName(ac appConfig, name string) (*instance, error) {
	cfg, err := newVmConfigForName(ac, name)
	if err != nil {
		return nil, err
	}
	return &instance{name: name, vmConfig: *cfg}, nil
}

func readInstanceForName(ac appConfig, name string) (*instance, error) {
	cfg, err := newVmConfigForName(ac, name)
	if err != nil {
		return nil, err
	}
	vm := &instance{name: name, vmConfig: *cfg}
	if err := vm.readConfig(); err != nil {
		return nil, err
	}
	return vm, nil
}

// FIXME(msolo) implement
func (vm *instance) lock() error {
	return nil
}

// FIXME(msolo) implement
func (vm *instance) unlock() error {
	return nil
}

// Get an IP address for the VM. This can cause a bit of a wait
// depending on the way we have to discover the address.
func (vm *instance) getIpAddr() (string, error) {
	if vm.vmConfig.IpAddr != "" {
		return vm.vmConfig.IpAddr, nil
	}
	return vm.getIpAddrFromVmtools()
}

// This can take a very long time for reasons I don't understand.
func (vm *instance) getIpAddrFromVmtools() (string, error) {
	cmd := exec.Command(vm.vmConfig.appConfig.VmrunBinaryPath, "-T", "fusion",
		"getGuestIPAddress", vm.vmConfig.vmxFile, "-wait")
	data, err := cmd.Output()
	if err != nil {
		logCmdError(cmd, err)
		return "", err
	}
	return string(bytes.TrimSpace(data)), nil
}

// Read the vmware dhcp lease file directly to find an IP address.
func (vm *instance) getIpAddrFromVmdhcp() (string, error) {
	fin, err := os.Open(vm.vmConfig.vmxFile)
	if err != nil {
		return "", err
	}
	defer fin.Close()
	scanner := bufio.NewScanner(fin)

	macAddr := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "ethernet0.generatedAddress") {
			fields := strings.Split(line, " = ")
			macAddr = fields[1][1 : len(fields[1])-1]
			break
		}
	}

	if macAddr == "" {
		return "", fmt.Errorf("no mac address found in vmx file: %s", vm.vmConfig.vmxFile)
	}

	for {
		ipAddr, err := vm.findIpAddrFromVmdhcp(macAddr)
		if err == noIpAddrForMacAddr {
			// Most of the time this just means we are waiting for vmware to do some internal allocation.
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err != nil {
			return "", err
		}
		return ipAddr, err
	}
}

func runCmd(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	_, err := cmd.CombinedOutput()
	if err != nil {
		logCmdError(cmd, err)
	}
	return err
}

func logCmdError(cmd *exec.Cmd, err error) {
	rc := 0
	stderr := ""
	if exitErr, ok := err.(*exec.ExitError); ok {
		rc = cmd.ProcessState.Sys().(syscall.WaitStatus).ExitStatus()
		stderr = string(exitErr.Stderr)
	}
	log.Printf("cmd failed: %v rc: %v\nstderr: %s", cmd.Args, rc, stderr)
}

var noIpAddrForMacAddr = errors.New("no ip address assignment found for mac addr in dchp lease file")

func (vm *instance) findIpAddrFromVmdhcp(macAddr string) (string, error) {
	leaseFile := "/var/db/vmware/vmnet-dhcpd-vmnet8.leases"
	fin, err := os.Open(leaseFile)
	if err != nil {
		return "", err
	}
	defer fin.Close()
	scanner := bufio.NewScanner(fin)

	// There might be multiple ip addr entries for a mac address. We want the last one.
	ipAddr := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		// lease 192.168.254.169 {
		if strings.HasPrefix(line, "lease") {
			leaseIpAddr := strings.Fields(line)[1]
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				// hardware ethernet 00:0c:29:ff:94:8f;
				if strings.HasPrefix(line, "hardware ethernet") {
					leaseMacAddr := strings.TrimRight(strings.Fields(line)[2], ";")
					if leaseMacAddr == macAddr {
						ipAddr = leaseIpAddr
					}
				} else if line == "}" {
					break
				}
			}
		}
	}
	if ipAddr == "" {
		return "", noIpAddrForMacAddr
	}
	return ipAddr, nil
}

func (vm *instance) sshConfigMap() map[string]string {
	return map[string]string{
		"ConnectTimeout":         "1",
		"ForwardAgent":           "yes",
		"IdentitiesOnly":         "yes",
		"LogLevel":               "error",
		"PasswordAuthentication": "no",
		"ProxyCommand":           "none",
		"StrictHostKeyChecking":  "no",
		"UserKnownHostsFile":     "/dev/null",
		// "ControlMaster":          "auto",
		// "ControlPath":            "/tmp/ssh_mux_%h_%p_%r",
		// "ControlPersist":         "15m",
	}
}

func (vm *instance) sshConfigArgs() ([]string, error) {
	cfgMap := vm.sshConfigMap()
	args := make([]string, 0, len(cfgMap))
	for k, v := range cfgMap {
		args = append(args, "-o"+k+"="+v)
	}
	sort.Strings(args)
	return args, nil
}

func (vm *instance) sshCmdArgs() ([]string, error) {
	cfgArgs, err := vm.sshConfigArgs()
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, 32)
	args = append(args, "ssh", "-F", "/dev/null")
	args = append(args, cfgArgs...)
	return args, nil
}

func (vm *instance) start() error {
	return runCmd(vm.vmConfig.appConfig.VmrunBinaryPath,
		"start", vm.vmConfig.vmxFile, "nogui")
}

func (vm *instance) stop(hard bool) error {
	args := []string{"stop", vm.vmConfig.vmxFile}
	if hard {
		args = append(args, "hard")
	}
	return runCmd(vm.vmConfig.appConfig.VmrunBinaryPath, args...)
}

func (vm *instance) writeConfig() error {
	data, err := json.Marshal(vm.vmConfig)
	if err != nil {
		return err
	}
	err = os.MkdirAll(path.Dir(vm.vmConfig.configFile), 0755)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(vm.vmConfig.configFile, data, 0644)
}

func (vm *instance) readConfig() error {
	data, err := ioutil.ReadFile(vm.vmConfig.configFile)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &vm.vmConfig)
}

func cmdLs(ctx context.Context, cfg *localConfig, args []string) {
	cmd := exec.Command(cfg.AppConfig.VmrunBinaryPath,
		"-T", "fusion", "list")
	data, err := cmd.Output()
	if err != nil {
		logCmdError(cmd, err)
		log.Fatalf("failed reading vms: %v", err)
	}
	for _, line := range strings.Split(string(bytes.TrimSpace(data)), "\n") {
		if strings.HasSuffix(line, ".vmx") {
			ext := path.Ext(line)
			name := path.Base(line[:len(line)-len(ext)])
			println(name, line)
		}
	}
}

func waitForSsh(ctx context.Context, ipAddr string) (ok bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	needNewline := false
	for deadline.Sub(time.Now()) > 0 {
		_, err := net.DialTimeout("tcp", ipAddr+":22", 1*time.Second)
		if err == nil {
			if needNewline {
				println("")
			}
			return true
		} else {
			print(".")
			needNewline = true
			time.Sleep(1 * time.Second)
		}
	}
	return false
}

func cmdStart(ctx context.Context, cfg *localConfig, args []string) {
	vm, err := readInstanceForName(cfg.AppConfig, cfg.Name)
	if os.IsNotExist(err) {
		cmdFetch(ctx, cfg, args)
		cmdClone(ctx, cfg, args)
		vm, err = readInstanceForName(cfg.AppConfig, cfg.Name)
	}
	if err != nil {
		log.Fatalf("failed reading config: %v", err)
	}
	log.Printf("Starting %s", vm.vmConfig.vmxFile)
	if err := vm.start(); err != nil {
		log.Fatalf("failed starting %s: %v", vm.vmConfig.vmxFile, err)
	}

	ipAddr, err := vm.getIpAddr()
	if err != nil {
		log.Fatalf("failed starting %s: %v", vm.vmConfig.vmxFile, err)
	}

	log.Printf("Waiting for ssh on %s", ipAddr)
	if ok := waitForSsh(ctx, ipAddr); ok {
		return
	}
	// Give up and wait for vmtools to give us the address.
	if _, err = vm.getIpAddrFromVmtools(); err != nil {
		log.Fatalf("failed starting %s: %v", vm.vmConfig.vmxFile, err)
	}
}

func cmdStop(ctx context.Context, cfg *localConfig, args []string) {
	flags := flag.NewFlagSet("stop", flag.ExitOnError)
	hard := flags.Bool("force", false, "Agressively stop the vm.")
	err := flags.Parse(args)
	if err != nil {
		log.Fatalf("failed: %v", err)
	}

	vm, err := readInstanceForName(cfg.AppConfig, cfg.Name)
	if err != nil {
		log.Fatalf("failed reading config: %v", err)
	}

	if err = vm.stop(*hard); err != nil {
		log.Fatalf("failed stop: %s", err)
	}
}

func cmdSuspend(ctx context.Context, cfg *localConfig, args []string) {
	vm, err := readInstanceForName(cfg.AppConfig, cfg.Name)
	if err != nil {
		log.Fatalf("failed reading config: %v", err)
	}
	err = syscall.Exec(vm.vmConfig.appConfig.VmrunBinaryPath, []string{"vmrun",
		"suspend", vm.vmConfig.vmxFile}, os.Environ())
	if err != nil {
		log.Fatalf("failed exec: %s", err)
	}
}

var errDeclined = errors.New("prompt declined")

func prompt(msg, affirmative string) error {
	fmt.Print(msg)
	reply := ""
	_, err := fmt.Scanf("%s\n", &reply)
	if err != nil {
		return err
	}
	if reply != affirmative {
		return errDeclined
	}
	return nil
}

func cmdRm(ctx context.Context, cfg *localConfig, args []string) {
	vm, err := readInstanceForName(cfg.AppConfig, cfg.Name)
	if err != nil {
		log.Fatalf("failed reading config: %v", err)
	}

	if err := prompt("Permanently remove vm and all data? [yes/NO] ", "yes"); err != nil {
		log.Fatalf("aborted: %v", err)
	}

	if err = vm.stop(true); err != nil {
		log.Fatalf("failed remove: %s", err)
	}

	if err = os.RemoveAll(vm.vmConfig.vmPath); err != nil {
		log.Fatalf("failed remove - partial data left in %s: %s", vm.vmConfig.vmPath, err)
	}
}

func archivePath(ac appConfig, bxc boxcar) string {
	return path.Join(ac.boxcarsDir(), path.Base(bxc.Url))
}

// Fetch a boxcar url and store it down to our local storage.
func cmdFetch(ctx context.Context, cfg *localConfig, args []string) {
	archive := archivePath(cfg.AppConfig, cfg.Boxcar)
	tmpArchivePath := path.Join(path.Dir(archive),
		fmt.Sprintf(".%s-%d", path.Base(archive), time.Now().UnixNano()))

	// If this file exists, we are probably good to go. Reverify?
	if _, err := os.Stat(archive); err == nil {
		hasher := sha256.New()
		fin, err := os.Open(archive)
		if err != nil {
			log.Fatalf("failed to fetch: %s", err)
		}
		defer fin.Close()
		if _, err := io.Copy(hasher, fin); err != nil {
			log.Fatalf("failed to fetch: %s", err)
		}
		sha256sum := fmt.Sprintf("%x", hasher.Sum(nil))
		if cfg.Boxcar.Sha256 != sha256sum {
			os.Remove(archive)
			log.Fatalf("failed to fetch: signature mismatch %s != %s", cfg.Boxcar.Sha256, sha256sum)
		}
		return
	}

	if err := os.MkdirAll(cfg.AppConfig.boxcarsDir(), 0755); err != nil {
		log.Fatalf("failed: %s", err)
	}

	fout, err := os.Create(tmpArchivePath)
	if err != nil {
		log.Fatal(err)
	}
	defer fout.Close()
	// Always try to remove the tempfile, a silent failer
	defer func() {
		if err := os.Remove(tmpArchivePath); err != nil && !os.IsNotExist(err) {
			log.Printf("warning unable to cleanup file: %s", err)
		}
	}()

	hasher := sha256.New()
	wr := io.MultiWriter(fout, hasher)
	tr := &http.Transport{}
	tr.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))
	cl := &http.Client{Transport: tr}

	log.Printf("fetching %s to %s ...", cfg.Boxcar.Url, archive)
	resp, err := cl.Get(cfg.Boxcar.Url)
	if err != nil {
		log.Fatalf("failed to fetch: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("failed to fetch: status %d", resp.StatusCode)
	}
	if _, err := io.Copy(wr, resp.Body); err != nil {
		log.Fatalf("failed to fetch: %s", err)
	}
	if err := fout.Sync(); err != nil {
		log.Fatalf("failed to fetch: %s", err)
	}
	if err := fout.Close(); err != nil {
		log.Fatalf("failed to fetch: %s", err)
	}
	sha256sum := fmt.Sprintf("%x", hasher.Sum(nil))
	if cfg.Boxcar.Sha256 != sha256sum {
		os.Remove(tmpArchivePath)
		log.Fatalf("failed to fetch: signature mismatch %s != %s", cfg.Boxcar.Sha256, sha256sum)
	} else {
		if err := os.Rename(tmpArchivePath, archive); err != nil {
			log.Fatal("failed to fetch: %s", err)
		}
	}
}

// For now a clone is simply unpacking a boxcar archive into a new directory.
// This might be less efficient if you have many of the same type of vm running,
// but I suspect that is uncommon and likely to have other problems.
func cmdClone(ctx context.Context, cfg *localConfig, args []string) {
	vm, err := newInstanceForName(cfg.AppConfig, cfg.Name)
	if err != nil {
		log.Fatalf("failed creating config: %s", err)
	}

	// We write the config once we are completely bootstrapped.
	if _, err := os.Stat(vm.vmConfig.configFile); err == nil {
		log.Fatalf("cannot overwrite existing vm: %s", vm.vmConfig.configFile)
	}

	// If there is a .vmx file without a config, it indicates a partial unpack.
	// Purge and start over.
	if _, err := os.Stat(vm.vmConfig.vmxFile); err == nil {
		if err = os.RemoveAll(vm.vmConfig.vmPath); err != nil {
			log.Fatalf("cannot remove existing vm: %s", vm.vmConfig.vmPath)
		}
	}

	if err := os.MkdirAll(cfg.AppConfig.vmsDir(), 0755); err != nil {
		log.Fatalf("failed cloning: %s", err)
	}

	if err := vm.lock(); err != nil {
		log.Fatalf("failed locking vm: %s", err)
	}

	defer func() {
		if err := vm.unlock(); err != nil {
			log.Fatalf("failed unlocking vm: %s", err)
		}
	}()

	boxcarUnpackFile := path.Join(cfg.AppConfig.boxcarsDir(),
		cfg.Boxcar.Name+".vmwarevm", ".hobo-unpacked")
	archive := archivePath(cfg.AppConfig, cfg.Boxcar)

	if _, err := os.Stat(boxcarUnpackFile); err != nil {
		log.Printf("Unpacking boxcar %s", archive)
		// if reuse_home_volume:
		//   cmd_args += ['--exclude', '*.vmwarevm/home*.vmdk']

		err := runCmd("/usr/bin/tar", "xzvf", archive, "-C", cfg.AppConfig.boxcarsDir())
		if err != nil {
			log.Fatalf("failed cloning: %s", err)
		}

		fi, err := os.Create(boxcarUnpackFile)
		if err != nil {
			log.Fatalf("failed cloning: %s", err)
		}
		fi.Close()
	}
	boxcarVmxFile := path.Join(cfg.AppConfig.boxcarsDir(),
		cfg.Boxcar.Name+".vmwarevm",
		cfg.Boxcar.Name+".vmx")

	if _, err := os.Stat(boxcarVmxFile); err != nil {
		log.Fatalf("invalid boxcar, missing vmx file: %s", boxcarVmxFile)
	}

	log.Printf("Cloning vm %s", archive)
	err = runCmd(cfg.AppConfig.VmrunBinaryPath,
		"-T", "fusion",
		"clone", boxcarVmxFile, vm.vmConfig.vmxFile,
		"full",
		"-cloneName="+cfg.Name)
	if err != nil {
		log.Fatalf("failed cloning: %s", err)
	}

	// FIXME(msolo) write out *.vmwarevm/hobo/config.json file to indicate unpacking is done?
	if !vm.vmConfig.TimeBootstrapped.IsZero() {
		log.Fatalf("failed bootstrap: already bootstrapped")
	}

	// Create a new insecure key that is specific to this instance.
	err = runCmd("/usr/bin/ssh-keygen",
		"-b", "1024",
		"-C", "hobo-insecure",
		"-N", "",
		"-f", vm.vmConfig.sshId,
	)
	if err != nil {
		log.Fatalf("failed bootstrap creating insecure key: %v", err)
	}

	// FIXME(msolo) Reuse start code.
	log.Printf("Starting vm for bootstrap %s", vm.vmConfig.vmxFile)
	if err := vm.start(); err != nil {
		log.Fatalf("failed bootstrap: %v", err)
	}
	log.Printf("Waiting for vm ip address %s", vm.vmConfig.vmxFile)
	ipAddr, err := vm.getIpAddr()
	if err != nil {
		log.Fatalf("failed bootstrap: %v", err)
	}

	log.Printf("Waiting for ssh on %s", ipAddr)
	if ok := waitForSsh(ctx, ipAddr); !ok {
		log.Printf("failed waiting %s: %v", ipAddr, vm.vmConfig.vmxFile)
		// Give up and wait for vmtools to give us the address.
		if _, err = vm.getIpAddrFromVmtools(); err != nil {
			log.Fatalf("failed starting %s: %v", vm.vmConfig.vmxFile, err)
		}
	}

	sshId := path.Join(cfg.AppConfig.HoboDir, "hobo-bootstrap-insecure")
	if _, err := os.Stat(sshId); err != nil {
		// FIXME(msolo) This should be WriteFileAtomic.
		if err := ioutil.WriteFile(sshId, []byte(bootstrapInsecurePrivateKey), 0600); err != nil {
			log.Fatalf("failed bootstrap: %v", err)
		}
	}

	sshCmdArgs, err := vm.sshCmdArgs()
	if err != nil {
		log.Fatalf("failed bootstrap: %v", err)
	}

	initialSshCmdArgs := make([]string, len(sshCmdArgs))
	copy(initialSshCmdArgs, sshCmdArgs)

	initialSshCmdArgs = append(initialSshCmdArgs, "-i", sshId, "hobo@"+ipAddr, "/bin/true")
	err = runCmd("/usr/bin/ssh", initialSshCmdArgs[1:]...)
	if err != nil {
		log.Fatalf("failed bootstrap initial ssh: %v", err)
	}

	scpKeyCmdArgs := make([]string, len(sshCmdArgs))
	copy(scpKeyCmdArgs, sshCmdArgs)
	scpKeyCmdArgs = append(scpKeyCmdArgs, "-i", sshId,
		vm.vmConfig.sshIdPub, "hobo@"+ipAddr+":.ssh/authorized_keys")
	err = runCmd("/usr/bin/scp", scpKeyCmdArgs[1:]...)
	if err != nil {
		log.Fatalf("failed bootstrap authorized keys: %v", err)
	}

	bashCmd := vm.vmConfig.boxcar.bootstrapBashScript()
	sshCmdArgs = append(sshCmdArgs, "-i", vm.vmConfig.sshId, "hobo@"+ipAddr, bashCmd)

	log.Printf("Bootstrapping guest on %s", ipAddr)
	cmd := exec.Command("/usr/bin/ssh", sshCmdArgs[1:]...)
	out, err := cmd.Output()
	// FIXME(msolo) Log cmd errors correctly - there is insufficient context by default.
	outlines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if strings.TrimSpace(outlines[len(outlines)-1]) == "hobo-bootstrap-ok" {
		vm.vmConfig.TimeBootstrapped = time.Now()
		vm.vmConfig.IpAddr = ipAddr
		err = vm.writeConfig()
	} else {
		logCmdError(cmd, err)
		log.Printf("bootstrap out: %s", out)
	}

	if err != nil {
		log.Fatalf("failed bootstrap: %v", err)
	}
	log.Printf("Instance running guest on %s", ipAddr)
}

func cmdIpAddr(ctx context.Context, cfg *localConfig, args []string) {
	vm, err := readInstanceForName(cfg.AppConfig, cfg.Name)
	if err != nil {
		log.Fatalf("failed reading config: %v", err)
	}
	// TODO(msolo) this won't work if the vm has not started up at least once.
	ip, err := vm.getIpAddr()
	if err != nil {
		log.Fatalf("failed finding ip addr: %v", err)
	}
	println(ip)
}

func cmdSsh(ctx context.Context, cfg *localConfig, args []string) {
	vm, err := readInstanceForName(cfg.AppConfig, cfg.Name)
	if err != nil {
		log.Fatalf("failed reading config: %v", err)
	}
	sshArgs, err := vm.sshCmdArgs()
	if err != nil {
		log.Fatalf("failed reading config: %v", err)
	}
	ip, err := vm.getIpAddr()
	if err != nil {
		log.Fatalf("failed finding ip addr: %v", err)
	}
	sshArgs = append(sshArgs, "-i", vm.vmConfig.sshId, "hobo@"+ip)
	syscall.Exec("/usr/bin/ssh", sshArgs, os.Environ())
}

func cmdSshConfig(ctx context.Context, cfg *localConfig, args []string) {
	vm, err := readInstanceForName(cfg.AppConfig, cfg.Name)
	if err != nil {
		log.Fatalf("failed reading config: %v", err)
	}

	ip, err := vm.getIpAddr()
	if err != nil {
		log.Fatalf("failed finding ip addr: %v", err)
	}

	vars := map[string]string{
		"ip_addr": ip,
		"user":    os.Getenv("LOGNAME"),
		"name":    vm.name,
	}
	getter := func(k string) string { return vars[k] }
	header := os.Expand("Host ${name} hobo-${name} ${ip_addr}", getter)
	lines := make([]string, 0, 16)
	for k, v := range vm.sshConfigMap() {
		lines = append(lines, "  "+k+" "+v)
	}
	sort.Strings(lines)
	println(header)
	println(strings.Join(lines, "\n"))
}

func cmdMakeBoxcar(ctx context.Context, cfg *localConfig, args []string) {
	if len(args) != 1 {
		log.Fatalf("failed: make-boxcar requires a path to a vmwarevm directory")
	}
	vmwarevmPath := path.Clean(args[0])
	if _, err := os.Stat(vmwarevmPath); err != nil {
		log.Fatalf("failed: make-boxcar requires a path to an existing vmwarevm directory: %s", vmwarevmPath)
	}
	rootVmdk := path.Join(vmwarevmPath, "root.vmdk")
	if _, err := os.Stat(rootVmdk); err != nil {
		log.Fatalf("failed: vmwarevm directory must have a root.vmdk: %s", rootVmdk)
	}

	err := runCmd(cfg.AppConfig.VmrunBinaryPath, "-T", "fusion", "start", vmwarevmPath, "nogui")
	if err != nil {
		log.Fatalf("failed starting boxcar %s: %s", rootVmdk, err)
	}
	err = runCmd(cfg.AppConfig.VmrunBinaryPath, "-T", "fusion", "stop", vmwarevmPath, "hard")
	if err != nil {
		log.Fatalf("failed stopping boxcar %s: %s", rootVmdk, err)
	}

	if err := os.RemoveAll(path.Join(vmwarevmPath, "caches")); err != nil {
		log.Fatalf("failed vmwarevm cleanup: %s", err)
	}

	removeFnames := []string{}
	logs, err := filepath.Glob(path.Join(vmwarevmPath, "*.log"))
	if err != nil {
		log.Fatalf("failed vmwarevm cleanup: %s", err)
	}
	removeFnames = append(removeFnames, logs...)
	locks, err := filepath.Glob(path.Join(vmwarevmPath, "*.lck"))
	if err != nil {
		log.Fatalf("failed vmwarevm cleanup: %s", err)
	}
	removeFnames = append(removeFnames, locks...)
	for _, fname := range removeFnames {
		if err := os.RemoveAll(fname); err != nil {
			log.Fatalf("failed vmwarevm cleanup: %s", err)
		}
	}

	log.Printf("Shrinking %s", rootVmdk)
	err = runCmd(cfg.AppConfig.VdiskManagerBinaryPath, "-d", rootVmdk)
	if err != nil {
		log.Fatalf("failed shrinking %s: %s", rootVmdk, err)
	}
	err = runCmd(cfg.AppConfig.VdiskManagerBinaryPath, "-k", rootVmdk)
	if err != nil {
		log.Fatalf("failed shrinking %s: %s", rootVmdk, err)
	}

	pigzCmd := exec.Command("pigz")
	pigzWr, err := pigzCmd.StdinPipe()
	if err != nil {
		log.Fatalf("failed compressing: %s", err)
	}
	fout, err := os.Create(vmwarevmPath + ".tgz")
	if err != nil {
		log.Fatalf("failed compressing: %s", err)
	}
	defer fout.Close()
	pigzCmd.Stdout = fout
	pigzErrC := make(chan error)
	go func() {
		pigzErrC <- pigzCmd.Run()
	}()

	log.Printf("Compressing %s", vmwarevmPath)
	tarCmd := exec.Command("tar", "cf", "-", "-C", path.Dir(vmwarevmPath), path.Base(vmwarevmPath))
	tarCmd.Stdout = pigzWr
	err = tarCmd.Run()
	if err != nil {
		logCmdError(tarCmd, err)
		log.Fatalf("failed compressing: %s", err)
	}
	pigzWr.Close()
	if err := <-pigzErrC; err != nil {
		logCmdError(pigzCmd, err)
		log.Fatalf("failed compressing: %s", err)
	}
	log.Printf("Created %s", fout.Name())
}

type cmdFunc func(ctx context.Context, cfg *localConfig, args []string)

const (
	timeFmt = "2006-01-02 15:04:05" // RFC3339 is hard to read at a glance
)

var (
	cmdMap map[string]cmdFunc
)

func findConfigFile(fname string) string {
	for _, name := range []string{fname, ".hobo", "$HOME/.hobo"} {
		name = os.ExpandEnv(name)
		if _, err := os.Stat(name); err == nil {
			return name
		}
	}
	return ""
}

func init() {
	cmdMap = map[string]cmdFunc{
		"start":   cmdStart,
		"stop":    cmdStop,
		"suspend": cmdSuspend,

		"ip-addr":    cmdIpAddr,
		"ssh":        cmdSsh,
		"ssh-config": cmdSshConfig,

		"ls": cmdLs,
		"rm": cmdRm,

		"fetch": cmdFetch,

		"make-boxcar": cmdMakeBoxcar,
	}
	log.SetFlags(log.Lshortfile | log.Ltime)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, doc)
		flag.PrintDefaults()
	}
}

func main() {
	timeout := flag.Duration("timeout", 0, "timeout for command execution (0 means unbounded)")
	hoboDir := flag.String("hobo-dir", "$HOME/.hobo.d", "directory for all hobo vm data")
	configFile := flag.String("config-file", "./.hobo", "local config file")

	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(1)
	}

	cmdName := args[0]
	args = args[1:]
	var cfg *localConfig
	var err error
	if cmdName == "help" {
		if len(args) > 0 {
			cmdName = args[0]
		}
	} else {
		cfgFname := ""
		if cmdName != "make-boxcar" {
			cfgFname = findConfigFile(*configFile)
		}
		cfg, err = newLocalConfigFromFile(cfgFname)
		if err != nil {
			if cfgFname == "" {
				log.Fatalf("fill out a .hobo file")
			}
			log.Fatalf("failed reading config: %s", err)
		}
	}
	if cfg.AppConfig.HoboDir == "" {
		cfg.AppConfig.HoboDir = *hoboDir
	}
	ctx := context.Background()
	if *timeout > 0 {
		ctx, _ = context.WithTimeout(ctx, *timeout)
	}
	if cmd, ok := cmdMap[cmdName]; ok {
		cmd(ctx, cfg, args)
	} else {
		flag.Usage()
		os.Exit(1)
	}
}