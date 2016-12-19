package gracemux

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"google.golang.org/grpc"

	"github.com/cockroachdb/cmux"
	"github.com/facebookgo/grace/gracenet"
)

var (
	verbose    = flag.Bool("gracelog", true, "Enable logging.")
	gracePid   = os.Getenv("GRACE_PID_FILE")
	didInherit = os.Getenv("LISTEN_FDS") != ""
	ppid       = os.Getppid()
)

type graceMux struct {
	cMux       cmux.CMux
	grpcServer *grpc.Server
	httpServer *http.Server

	net              *gracenet.Net
	listener, gl, hl net.Listener
	errors           chan error
	pidfile          string
}

func NewGraceMux(net, addr string) *graceMux {
	gc := &graceMux{
		net: &gracenet.Net{},

		//for  StartProcess error.
		errors:  make(chan error),
		pidfile: gracePid,
	}
	l, err := gc.net.Listen(net, addr)
	if err != nil {
		panic(err)
	}
	gc.listener = l
	gc.cMux = cmux.New(l)
	gc.gl = gc.cMux.Match(cmux.HTTP2HeaderField("content-type", "application/grpc"))
	gc.hl = gc.cMux.Match(cmux.HTTP1Fast())
	return gc
}

func (gc *graceMux) SetGrpcServer(s *grpc.Server) {
	gc.grpcServer = s
}

func (gc *graceMux) SetHttpServer(s *http.Server) {
	gc.httpServer = s
}

func (gc *graceMux) serve() {
	go gc.grpcServer.Serve(gc.gl)
	go gc.httpServer.Serve(gc.hl)
	go gc.cMux.Serve()
}

func (gc *graceMux) Serve() error {

	if *verbose {
		if didInherit {
			if ppid == 1 {
				log.Printf("Listening on init activated %s\n", pprintAddr(gc.listener))
			} else {
				const msg = "Graceful handoff of %s with new pid %d replace old pid %d"
				log.Printf(msg, pprintAddr(gc.listener), os.Getpid(), ppid)
			}
		} else {
			const msg = "Serving %s with pid %d\n"
			log.Printf(msg, pprintAddr(gc.listener), os.Getpid())
		}
	}

	err := gc.doWritePid(os.Getpid())
	if err != nil {
		log.Println(err)
	}

	gc.serve()

	if didInherit && ppid != 1 {
		if err := syscall.Kill(ppid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("failed to close parent: %s", err)
		}
	}

	waitdone := make(chan struct{})
	go func() {
		defer close(waitdone)
		gc.wait()
	}()

	select {
	case err := <-gc.errors:
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

func (gc graceMux) GracefulStop() {

}

func (gc *graceMux) wait() {
	var wg sync.WaitGroup
	wg.Add(1)
	go gc.signalHandler(&wg)
	wg.Wait()
}

func (gc *graceMux) signalHandler(wg *sync.WaitGroup) {
	ch := make(chan os.Signal, 10)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR2)
	for {
		sig := <-ch
		log.Printf("signal: %s has received", sig)
		switch sig {
		case syscall.SIGINT, syscall.SIGTERM:
			defer wg.Done()
			signal.Stop(ch)
			gc.grpcServer.GracefulStop()
			return
		case syscall.SIGUSR2:
			if _, err := gc.net.StartProcess(); err != nil {
				gc.errors <- err
			}
		}
	}
}

func (gc *graceMux) doWritePid(pid int) (err error) {
	if gc.pidfile == "" {
		return nil
	}

	pf, err := os.Create(gc.pidfile)
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
