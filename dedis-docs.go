package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func main() {
	fmt.Println("Starting Up")
	installGo()
	p := new(Proxy)
	fmt.Println("Running proxy")
	go p.run()
	fmt.Println("handling root")
	http.Handle("/", p)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

const (
	repoURL = "https://go.googlesource.com/"
	metaURL = "https://go.googlesource.com/?b=master&format=JSON"
)

var (
	tempDir = os.TempDir()
	goDir   = tempDir + "go"
	goBin   = tempDir + "go/bin/go"
	gopathA = tempDir + "gopathA"
	gopathB = tempDir + "gopathB"
)

func installGo() {
	// install and setup go: fresh install
	tempDir = os.TempDir()
	fmt.Println("Installing Go")
	dir := tempDir
	if err := os.MkdirAll(dir, 0755); err != nil {
		panic(fmt.Sprintf("unable to make tempdir: %v", err))
	}

	fmt.Println("Checking out go source")
	goDir := filepath.Join(dir, "go")
	if err := checkout(repoURL+"go", goDir); err != nil {
		panic(fmt.Sprintf("unable to checkout go source: %v", err))
	}

	fmt.Println("Making Go")
	m := exec.Command(filepath.Join(goDir, "src/make.bash"))
	m.Dir = filepath.Join(goDir, "src")
	if err := runErr(m); err != nil {
		panic(fmt.Sprintf("unable to make go source: %v", err))
	}
	fmt.Println("Go Made")
}

type Side string

const (
	A = "gopathA"
	B = "gopathB"
)

// proxy forwards traffic to the most recent running godoc
type Proxy struct {
	mu    sync.Mutex
	proxy http.Handler
	cur   Side
	side  Side
	err   error
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Serving HTTP")
	if r.URL.Path == "/_buildstatus" {
		p.serveStatus(w, r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	proxy := p.proxy
	err := p.err
	if proxy == nil {
		s := "docs are generating"
		if err != nil {
			s = err.Error()
		}
		http.Error(w, s, http.StatusInternalServerError)
		return
	}
	proxy.ServeHTTP(w, r)
}

func (p *Proxy) serveStatus(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(w, "side=%v\ncurrent=%v\nerror=%v\n", p.side, p.cur, p.err)
}

func runErr(cmd *exec.Cmd) error {
	cmd.Env = append(cmd.Env, "PATH="+os.Getenv("PATH"))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("%s\n%v", err)
	}
	return nil
}

func checkout(repo, path string) error {
	runErr(exec.Command("rm", "-rf", path))
	if _, err := os.Stat(filepath.Join(path, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		fmt.Println("Git Clone")
		if err := runErr(exec.Command("git", "clone", repo, path)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	fmt.Println("Made directories and finished git clone")
	// Pull down changes and update to hash.
	cmd := exec.Command("git", "fetch")
	cmd.Dir = path
	return runErr(cmd)
}

func (p *Proxy) updateDedis(newside Side) error {
	// we are updating the dedis repositories therefore we switch sides from
	// wherever the current proxy is pointed
	fmt.Println("Updating Dedis")
	gopath := gopathA
	if newside == B {
		gopath = gopathB
	}
	fmt.Println("Getting tools")
	toolsInstall := exec.Command(goBin, "get", "-u", "golang.org/x/tools/...")
	toolsInstall.Env = []string{"GOPATH=" + gopath}
	if err := runErr(toolsInstall); err != nil {
		return err
	}
	fmt.Println("Getting crypto and prifi")
	// install crypto and prifi repositories
	dedisInstall := exec.Command(goBin, "get", "-u", "github.com/dedis/crypto/...", "github.com/dedis/prifi/...")
	dedisInstall.Env = []string{"GOPATH=" + gopath}
	runErr(dedisInstall)
	return nil
}

func (p *Proxy) run() {
	// poll github for new updates
	p.side = B
	if err := p.updateDedis(A); err != nil {
		panic("error updating dedis: " + err.Error())
	}
	hostport, err := p.startGoDoc(A)
	if err != nil {
		panic("error starting godoc: " + err.Error())
	}
	u, err := url.Parse(fmt.Sprintf("http://%v/", hostport))
	if err != nil {
		log.Println(err)
		p.err = err
		return
	}
	p.proxy = httputil.NewSingleHostReverseProxy(u)

	p.side = A
	for {
		p.poll()
		time.Sleep(30 * time.Second)
	}
}

func (p *Proxy) startGoDoc(side Side) (hostport string, err error) {
	fmt.Println("Starting godoc")
	godocBin := filepath.Join(goDir, "bin/godoc")
	hostport = "localhost:8081"
	gopath := gopathA
	if side == B {
		hostport = "localhost:8082"
		gopath = gopathB
	}
	godoc := exec.Command(godocBin, "-http="+hostport, "-analysis=type,pointer")
	godoc.Env = []string{"GOPATH=" + gopath, "PATH=" + os.Getenv("PATH")}
	godoc.Stdout = os.Stdout
	stdout, err := godoc.StderrPipe()
	if err != nil {
		fmt.Println("Error getting stdout")
		return "", err
	}
	fmt.Println("Starting up Godoc")
	if err := godoc.Start(); err != nil {
		fmt.Println("Error starting")
		return "", err
	}
	r := bufio.NewReader(stdout)
	for {
		line, _, err := r.ReadLine()
		if err != nil {
			fmt.Println("Error Reading Line")
			return "", err
		}
		// analysis done
		fmt.Println("LINE:", string(line))
		if strings.Contains(string(line), "Analysis complete") {
			fmt.Println("Done starting godoc")
			return hostport, nil
		}
	}
}

func (p *Proxy) poll() {
	gopath := gopathA
	if p.side == B {
		gopath = gopathB
	}
	// check if there is a change in the current directory
	pull := exec.Command(goBin, "fetch", "--dry-run")
	pull.Dir = filepath.Join(gopath, "github.com/dedis/crypto")
	output, err := pull.Output()
	if err != nil {
		return
	}
	pull = exec.Command(goBin, "fetch", "--dry-run")
	pull.Dir = filepath.Join(gopath, "github.com/dedis/prifi")
	output2, err := pull.Output()
	if err != nil {
		return
	}

	// if output is not empty then something was changed
	if string(output) != "" || string(output2) != "" {
		fmt.Println("Something changed")
		var newSide Side = A
		if p.side == A {
			p.side = B
		}
		err := p.updateDedis(newSide)
		if err != nil {
			return
		}
		hostport, err := p.startGoDoc(newSide)
		if err != nil {
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		u, err := url.Parse(fmt.Sprintf("http://%v/", hostport))
		if err != nil {
			log.Println(err)
			p.err = err
			return
		}
		p.side = newSide
		p.proxy = httputil.NewSingleHostReverseProxy(u)
	}
}
