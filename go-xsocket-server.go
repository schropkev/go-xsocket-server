package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coreos/go-systemd/v22/journal"
)

type Request struct {
	Signature uint32
	Domain    int32
	Type      int32
	Protocol  int32
}

type Response struct {
	Signature uint32
	Error     int32
}

const (
	XS_PROTOCOL_REQUEST  = 0x58533031
	XS_PROTOCOL_RESPONSE = 0x58533032
)

var (
	useSystemd bool
	reloading   atomic.Bool
	reqCounter  atomic.Uint64
)

func cPriorityName(p journal.Priority) string {
	switch p {
	case journal.PriEmerg:
		return "EMERG"
	case journal.PriAlert:
		return "ALERT"
	case journal.PriCrit:
		return "CRIT"
	case journal.PriErr:
		return "ERR"
	case journal.PriWarning:
		return "WARNING"
	case journal.PriNotice:
		return "NOTICE"
	case journal.PriInfo:
		return "INFO"
	case journal.PriDebug:
		return "DEBUG"
	default:
		return "UNKNOWN"
	}
}

func cJournalSend(priority journal.Priority, msg string, fields map[string]string) {
	if !journal.Enabled() {
		// fallback identical to C stderr style
		fmt.Printf("[%s] %s\n", cPriorityName(priority), msg)
		return
	}

	payload := map[string]string{
		"MESSAGE":           msg,
		"PRIORITY":          strconv.Itoa(int(priority)),
		"SYSLOG_IDENTIFIER": "go-xsocket-server",
	}

	for k, v := range fields {
		payload[strings.ToUpper(k)] = v
	}

	_ = journal.Send(msg, priority, payload)
}

func cInfo(msg string, f map[string]string)  { cJournalSend(journal.PriInfo, msg, f) }
func cWarn(msg string, f map[string]string)  { cJournalSend(journal.PriWarning, msg, f) }
func cErr(msg string, f map[string]string)   { cJournalSend(journal.PriErr, msg, f) }
func cDebug(msg string, f map[string]string) { cJournalSend(journal.PriDebug, msg, f) }

func xsocketBanner(id uint64) {
	fmt.Printf("# go-xsocket-server @%d\n", id)
}

func xsocketRequestLog(domain, typ, proto, pid, uid, gid int) {
	fmt.Printf(
		"socket request: domain=%d, type=%d, protocol=%d, pid=%d, uid=%d, gid=%d\n",
		domain, typ, proto, pid, uid, gid,
	)
}

func xsocketSeparator() {
	fmt.Println("---------------")
}

func sdNotify(state string) {
	if !useSystemd {
		return
	}

	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return
	}

	addr := &syscall.SockaddrUnix{Name: socket}

	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return
	}
	defer syscall.Close(fd)

	_ = syscall.Sendto(fd, []byte(state), 0, addr)
}

func watchdogLoop() {
	if !useSystemd {
		return
	}

	v := os.Getenv("WATCHDOG_USEC")
	if v == "" {
		return
	}

	usec, err := strconv.Atoi(v)
	if err != nil || usec <= 0 {
		return
	}

	t := time.NewTicker(time.Duration(usec/2) * time.Microsecond)
	defer t.Stop()

	for range t.C {
		sdNotify("WATCHDOG=1")
	}
}

func recvPacket(fd int) (Request, *syscall.Ucred, int, error) {
	var req Request

	buf := make([]byte, 16)
	oob := make([]byte, 256)

	n, oobn, _, _, err := syscall.Recvmsg(fd, buf, oob, 0)
	if err != nil {
		return req, nil, -1, err
	}
	if n < 16 {
		return req, nil, -1, syscall.EINVAL
	}

	req.Signature = binary.BigEndian.Uint32(buf[0:4])
	req.Domain = int32(binary.BigEndian.Uint32(buf[4:8]))
	req.Type = int32(binary.BigEndian.Uint32(buf[8:12]))
	req.Protocol = int32(binary.BigEndian.Uint32(buf[12:16]))

	var cred *syscall.Ucred
	recvfd := -1

	scms, _ := syscall.ParseSocketControlMessage(oob[:oobn])

	for _, scm := range scms {
		switch scm.Header.Type {
		case syscall.SCM_CREDENTIALS:
			ucred, _ := syscall.ParseUnixCredentials(&scm)
			cred = ucred

		case syscall.SCM_RIGHTS:
			fds, _ := syscall.ParseUnixRights(&scm)
			if len(fds) > 0 {
				recvfd = fds[0]
			}
		}
	}

	return req, cred, recvfd, nil
}

func sendPacket(fd int, resp Response, sendfd int) error {
	out := make([]byte, 8)
	binary.BigEndian.PutUint32(out[0:4], resp.Signature)
	binary.BigEndian.PutUint32(out[4:8], uint32(resp.Error))

	var oob []byte
	if sendfd >= 0 {
		oob = syscall.UnixRights(sendfd)
	}

	return syscall.Sendmsg(fd, out, oob, nil, syscall.MSG_NOSIGNAL)
}

func createSocket(domain, typ, proto int) (int, error) {
	return syscall.Socket(domain, typ|syscall.SOCK_CLOEXEC, proto)
}

func handle(conn int) {
	id := reqCounter.Add(1)

	req, cred, _, err := recvPacket(conn)
	if err != nil {
		cWarn("recvPacket failed", map[string]string{"error": err.Error()})
		return
	}

	if req.Signature != XS_PROTOCOL_REQUEST {
		cWarn("invalid signature", map[string]string{"req_id": strconv.FormatUint(id, 10)})
		return
	}

	uid, gid, pid := 0, 0, 0
	if cred != nil {
		uid, gid, pid = int(cred.Uid), int(cred.Gid), int(cred.Pid)
	}

	xsocketBanner(id)
	xsocketRequestLog(
		int(req.Domain),
		int(req.Type),
		int(req.Protocol),
		pid, uid, gid,
	)
	xsocketSeparator()

	cInfo("socket request received", map[string]string{
		"req_id":   strconv.FormatUint(id, 10),
		"domain":   strconv.Itoa(int(req.Domain)),
		"type":     strconv.Itoa(int(req.Type)),
		"protocol": strconv.Itoa(int(req.Protocol)),
		"pid":      strconv.Itoa(pid),
		"uid":      strconv.Itoa(uid),
		"gid":      strconv.Itoa(gid),
	})

	fd, err := createSocket(int(req.Domain), int(req.Type), int(req.Protocol))

	var resp Response
	resp.Signature = XS_PROTOCOL_RESPONSE

	if err != nil {
		if e, ok := err.(syscall.Errno); ok {
			resp.Error = int32(e)
		} else {
			resp.Error = int32(syscall.EIO)
		}
		fd = -1

		cErr("socket creation failed", map[string]string{
			"error": err.Error(),
		})
	} else {
		resp.Error = 0
		cDebug("socket created", map[string]string{
			"fd": strconv.Itoa(fd),
		})
	}

	_ = sendPacket(conn, resp, fd)

	if fd >= 0 {
		_ = syscall.Close(fd)
	}
}

func serve(fd int) {
	for {
		nfd, _, err := syscall.Accept4(fd, syscall.SOCK_CLOEXEC)
		if err != nil {
			continue
		}
		go handle(nfd)
	}
}

func setupSocket(path string) int {
	addr := &syscall.SockaddrUnix{Name: path}

	_ = syscall.Unlink(path)

	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		panic(err)
	}

	if err := syscall.Bind(fd, addr); err != nil {
		panic(err)
	}

	if err := syscall.Listen(fd, 32); err != nil {
		panic(err)
	}

	return fd
}

func main() {
	chmod := flag.String("chmod", "", "unix socket chmod")
	systemd := flag.Bool("systemd", false, "enable systemd integration")
	flag.Parse()

	useSystemd = *systemd

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: go-xsocket-server [--chmod 0660] [--systemd] <path|@abstract>")
		os.Exit(2)
	}

	target := flag.Arg(0)

	isFile := !strings.HasPrefix(target, "@")

	var path string
	if strings.HasPrefix(target, "@") {
		path = "\x00" + target[1:]
	} else {
		path = target
		_ = os.Remove(target)
	}

	sdNotify("STATUS=Starting")
	cInfo("server starting", nil)

	fd := setupSocket(path)

	if isFile && *chmod != "" {
		if v, err := strconv.ParseUint(*chmod, 8, 32); err == nil {
			_ = os.Chmod(target, os.FileMode(v))
		}
	}

	go watchdogLoop()

	sdNotify("READY=1")
	cInfo("server ready", nil)

	sig := make(chan os.Signal, 1)
	reload := make(chan os.Signal, 1)

	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	signal.Notify(reload, syscall.SIGHUP)

	go serve(fd)

	for {
		select {

		case <-reload:
			reloading.Store(true)
			sdNotify("RELOADING=1")
			cInfo("reloading configuration", nil)
			time.Sleep(200 * time.Millisecond)
			reloading.Store(false)
			sdNotify("READY=1")

		case <-sig:
			cInfo("server stopping", nil)
			sdNotify("STOPPING=1")
			_ = syscall.Close(fd)
			return
		}
	}
}
