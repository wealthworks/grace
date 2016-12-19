package graceserver

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"github.com/facebookgo/grace/gracenet"
)

var (
	verbose    = flag.Bool("gracelog", true, "Enable logging.")
	gracePid   = os.Getenv("GRACE_PID_FILE")
	didInherit = os.Getenv("LISTEN_FDS") != ""
	ppid       = os.Getppid()
)

type Server interface {
	Serve(l net.Listener)
	Stop()
}

type graceServer struct {
	server   Server
	net      *gracenet.Net
	listener net.Listener
	errors   chan error
	pidfile  string
}

func NewGraceServer(s Server, net, addr string) *graceServer {
	gs := &graceServer{
		server: s,
		net:    &gracenet.Net{},

		//for  StartProcess error.
		errors:  make(chan error),
		pidfile: gracePid,
	}
	l, err := gs.net.Listen(net, addr)
	if err != nil {
		panic(err)
	}
	gs.listener = l
	return gs
}

func (gs *graceServer) Serve() error {

	if *verbose {
		if didInherit {
			if ppid == 1 {
				log.Printf("Listening on init activated %s\n", pprintAddr(gs.listener))
			} else {
				const msg = "Graceful handoff of %s with new pid %d replace old pid %d"
				log.Printf(msg, pprintAddr(gs.listener), os.Getpid(), ppid)
			}
		} else {
			const msg = "Serving %s with pid %d\n"
			log.Printf(msg, pprintAddr(gs.listener), os.Getpid())
		}
	}

	err := gs.doWritePid(os.Getpid())
	if err != nil {
		log.Println(err)
	}

	go gs.server.Serve(gs.listener)

	if didInherit && ppid != 1 {
		if err := syscall.Kill(ppid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("failed to close parent: %s", err)
		}
	}

	waitdone := make(chan struct{})
	go func() {
		defer close(waitdone)
		gs.wait()
	}()

	select {
	case err := <-gs.errors:
		if err == nil {
			panic("unexpected nil error")
		}
		return err
	case <-waitdone:
		if *verbose {
			log.Printf("Exiting pid %d.", os.Getpid())
		}
		return nil
	}
}

func (gs *graceServer) wait() {
	var wg sync.WaitGroup
	wg.Add(1)
	go gs.signalHandler(&wg)
	wg.Wait()
}

func (gs *graceServer) signalHandler(wg *sync.WaitGroup) {
	ch := make(chan os.Signal, 10)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR2)
	for {
		sig := <-ch
		log.Printf("signal: %s has received", sig)
		switch sig {
		case syscall.SIGINT, syscall.SIGTERM:
			defer wg.Done()
			signal.Stop(ch)
			gs.server.Stop()
			return
		case syscall.SIGUSR2:
			if _, err := gs.net.StartProcess(); err != nil {
				gs.errors <- err
			}
		}
	}
}

func (gs *graceServer) doWritePid(pid int) (err error) {
	if gs.pidfile == "" {
		return nil
	}

	pf, err := os.Create(gs.pidfile)
	defer pf.Close()
	if err != nil {
		log.Println(err)
	}

	_, err = pf.WriteString(strconv.Itoa(pid))
	if err != nil {
		log.Println(err)
	}
	return err
}

func pprintAddr(l net.Listener) []byte {
	var out bytes.Buffer
	fmt.Fprint(&out, l.Addr())
	return out.Bytes()
}
