package main

import (
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"
)

var (
	real          = flag.Bool("real", false, "run the deletions")
	readDirectory = os.ReadDir
	entryInfo     = func(entry os.DirEntry) (os.FileInfo, error) { return entry.Info() }
	removePath    = os.RemoveAll
	directorySize = DirSize
	fatalLog      = log.Fatal
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fatalLog(err)
	}
}

func run() error {
	files, err := readDirectory("_repos/src")
	if err != nil {
		return err
	}

	for _, f := range files {
		if f.Name() != "github.com" {
			continue
		}
		if f.IsDir() {
			dirs, err := readDirectory("_repos/src/" + f.Name())
			if err != nil {
				return err
			}
			for _, d := range dirs {
				repos, err := readDirectory("_repos/src/" + f.Name() + "/" + d.Name())
				if err != nil {
					return err
				}
				for _, repo := range repos {
					path := "_repos/src/" + f.Name() + "/" + d.Name() + "/" + repo.Name()

					info, err := entryInfo(d)
					if err != nil {
						return err
					}

					if time.Since(info.ModTime()) > 30*24*time.Hour {
						if *real {
							log.Printf("Deleting %s (repo is old)...", path)
							removePath(path)
							continue
						} else {
							log.Printf("Would delete %s (repo is old)", path)
						}
					}

					size, err := directorySize(path)
					if err != nil {
						return err
					}
					if size < 20*1000*1000 {
						if *real {
							log.Printf("Deleting %s (dir size < 20M)...", path)
							removePath(path)
						} else {
							log.Printf("Would delete %s (dir size < 20M)", path)
						}
					}
				}
			}
		}
	}
	return nil
}

// DirSize returns the size of a directory
func DirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			size += info.Size()
		}
		return err
	})

	return size, err
}
