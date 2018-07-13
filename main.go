package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"

	fsnotify "gopkg.in/fsnotify.v1"
)

type Config struct {
	Root      string `json:"Root"`
	Recursive bool   `json:"Recursive"`
	Entry     string `json:"Entry"`
}

var proc *os.Process

func build(entry string) ([]string, error) {
	goPath, ok := os.LookupEnv("GOPATH")
	if !ok {
		return nil, errors.New("$GOPATH environment variable not exists")
	}
	cmd := exec.Command("vgo", "build", "-o", "main", entry)
	err := cmd.Run()
	if err != nil {
		return nil, err
	}
	f, err := os.Open("go.sum")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	l := make([]string, 0, 64)
	reader := bufio.NewReader(f)
	for line, err := reader.ReadBytes('\n'); err == nil; line, err = reader.ReadBytes('\n') {
		path := string(bytes.Split(line, []byte(" "))[0])
		if path != "" {
			l = append(l, filepath.Join(goPath, "src", path))
		}
	}
	return l, nil
}

// func run() error {
// 	cmd := exec.Command("./main")
// 	cmd.Stdout = os.Stdout
// 	cmd.Stderr = os.Stderr
// 	err := cmd.Start()
// 	if err != nil {
// 		return err
// 	}
// 	proc = cmd.Process
// 	log.Printf("main process is running (pid: %d)\n", proc.Pid)
// 	return nil
// }

func run() (chan interface{}, error) {
	cmd := exec.Command("./main")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Start()
	if err != nil {
		return nil, err
	}
	proc = cmd.Process
	procStatChan := make(chan interface{})
	go func() {
		defer func() {
			recover()
			close(procStatChan)
		}()
		_, err := proc.Wait()
		if err != nil {
			log.Println(strings.Repeat("=", 60))
			log.Printf("exit error:(%s)", err.Error())
			log.Println(strings.Repeat("=", 60))
		}
		log.Println("main process has exited")
	}()
	log.Printf("main process is running (pid: %d)\n", proc.Pid)
	return procStatChan, nil
}

func listSubDir(root string) ([]string, error) {
	dirs := make([]string, 0, 64)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

func addWatchPath(watcher *fsnotify.Watcher, pathList []string, recursive bool) error {
	if recursive {
		for _, root := range pathList {
			dirs, err := listSubDir(root)
			if err != nil {
				return err
			}
			for _, dir := range dirs {
				err := watcher.Add(dir)
				if err != nil {
					return err
				}
			}
		}
	} else {
		for _, dir := range pathList {
			err := watcher.Add(dir)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func newWatcher(deps []string, recursive bool) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	err = addWatchPath(watcher, []string{pwd}, recursive)
	if err != nil {
		return nil, err
	}
	err = addWatchPath(watcher, deps, true)
	if err != nil {
		return nil, err
	}
	return watcher, nil
}

func handleInterrupt(stopChan chan interface{}) {
	sigChan := make(chan os.Signal)
	signal.Notify(sigChan, os.Interrupt)
	<-sigChan
	log.Println("got interrupt signal")
	signal.Stop(sigChan)
	close(sigChan)
	close(stopChan)
}

func kill() error {
	if proc != nil {
		log.Printf("stopping process %d\n", proc.Pid)
		err := proc.Kill()
		if err != nil {
			return fmt.Errorf("failed to stop process %d due to (%s)", proc.Pid, err.Error())
		}
		proc = nil
	}
	return nil
}

// func loop(entry string, recursive bool, watcher *fsnotify.Watcher, stopChan chan interface{}) {
// 	for {
// 		select {
// 		case err := <-watcher.Errors:
// 			panic(err)
// 		case event := <-watcher.Events:
// 			if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Write) > 0 {
// 				log.Println("detected source file change")
// 				err := kill()
// 				if err != nil {
// 					log.Println(err)
// 					return
// 				}
// 				deps, err := build(entry)
// 				if err != nil {
// 					log.Println(err)
// 					continue
// 				}
// 				if err := watcher.Close(); err != nil {
// 					log.Println(err)
// 					return
// 				}
// 				watcher, err = newWatcher(deps, recursive)
// 				if err != nil {
// 					log.Println(err)
// 					return
// 				}
// 				if err := run(); err != nil {
// 					log.Println(err)
// 					continue
// 				}
// 			}
// 		case <-stopChan:
// 			err := kill()
// 			if err != nil {
// 				log.Println(err)
// 			}
// 			watcher.Close()
// 			log.Println("closed")
// 			return
// 		}
// 	}
// }

func loop(entry string, recursive bool, watcher *fsnotify.Watcher, procChan chan interface{}, stopChan chan interface{}) {
	for {
		select {
		case err := <-watcher.Errors:
			panic(err)
		case event := <-watcher.Events:
			if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Write) > 0 {
				log.Println("detected source file change")
				err := kill()
				if err != nil {
					log.Println(err)
					return
				}
				<-procChan
				deps, err := build(entry)
				if err != nil {
					log.Println(err)
					continue
				}
				if err := watcher.Close(); err != nil {
					log.Println(err)
					return
				}
				watcher, err = newWatcher(deps, recursive)
				if err != nil {
					log.Println(err)
					return
				}
				if newProcChan, err := run(); err != nil {
					log.Println(err)
					continue
				} else {
					procChan = newProcChan
				}
			}
		case <-procChan:
			newProcChan, err := run()
			if err != nil {
				log.Println(err)
				continue
			}
			procChan = newProcChan
		case <-stopChan:
			err := kill()
			if err != nil {
				log.Println(err)
			}
			watcher.Close()
			log.Println("closed")
			return
		}
	}
}

// func main() {
// 	var recursive bool
// 	var entry string
// 	flag.BoolVar(&recursive, "r", true, "recursive watch")
// 	flag.StringVar(&entry, "e", "main.go", "specific the main entry")
// 	flag.Parse()
// 	deps, err := build(entry)
// 	if err != nil {
// 		panic(err)
// 	}
// 	watcher, err := newWatcher(deps, recursive)
// 	if err != nil {
// 		panic(err)
// 	}
// 	err = run()
// 	if err != nil {
// 		panic(err)
// 	}
// 	stopChan := make(chan interface{})
// 	go handleInterrupt(stopChan)
// 	loop(entry, recursive, watcher, stopChan)
// }

func main() {
	var recursive bool
	var entry string
	flag.BoolVar(&recursive, "r", true, "recursive watch")
	flag.StringVar(&entry, "e", "main.go", "specific the main entry")
	flag.Parse()
	deps, err := build(entry)
	if err != nil {
		panic(err)
	}
	watcher, err := newWatcher(deps, recursive)
	if err != nil {
		panic(err)
	}
	procChan, err := run()
	if err != nil {
		panic(err)
	}
	stopChan := make(chan interface{})
	go handleInterrupt(stopChan)
	loop(entry, recursive, watcher, procChan, stopChan)
}
