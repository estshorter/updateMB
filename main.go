package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/estshorter/timeout"
)

// Configs includes MB update time
type Configs struct {
	MBPatchURL     string    `json:"mbPatchURL"`
	MBPath         string    `json:"mbPath"`
	TargetFileName string    `json:"targetFileName"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func readConfigs(path string) (*Configs, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var configs Configs
	if err := json.Unmarshal(content, &configs); err != nil {
		return nil, err
	}
	return &configs, nil
}

func writeUpdatedAt(filename string, configs *Configs) error {
	jsonText, err := json.MarshalIndent(configs, "", "\t")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, jsonText, os.ModePerm)
}

func scrapeMBUpdatedAt(mbPatchURL, targetFileName string) (*time.Time, error) {
	resp, err := http.Get(mbPatchURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	mbSiteTimeStampRaw := doc.Find("a[href=\"" + targetFileName + "\"]").Parent().Next().Text()
	mbSiteTimeStamp, err := time.Parse("2006-01-02 15:04", strings.TrimSpace(mbSiteTimeStampRaw))
	if err != nil {
		return nil, err
	}
	return &mbSiteTimeStamp, nil
}

func needMBUpdate(configs *Configs) (bool, *time.Time, error) {
	updatedAtSite, err := scrapeMBUpdatedAt(configs.MBPatchURL, configs.TargetFileName)
	if err != nil {
		return false, nil, err
	}
	updatedAtFile := configs.UpdatedAt
	if updatedAtSite.After(updatedAtFile) {
		return true, updatedAtSite, nil
	}
	return false, updatedAtSite, nil
}

func downloadFileToMemory(url string) (*bytes.Reader, int, error) {
	// Get the data
	resp, err := http.Get(url)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(body), len(body), nil
}

func extractUpdatedFiles(zipReader io.ReaderAt, size int, dest string) (int, error) {
	cntExtract := 0

	r, err := zip.NewReader(zipReader, int64(size))
	if err != nil {
		return cntExtract, err
	}
	// defer r.Close()

	for _, f := range r.File {
		// Store filename/path for returning and using later on
		fpath := filepath.Join(dest, f.Name)

		// Check for ZipSlip. More Info: http://bit.ly/2MsjAWE
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			return cntExtract, fmt.Errorf("%s: illegal file path", fpath)
		}

		if f.FileInfo().IsDir() {
			if !exists(fpath) {
				os.MkdirAll(fpath, os.ModePerm)
			}
			continue
		}

		if extracted, err := extractUpdatedFile(f, fpath); err != nil {
			return cntExtract, err
		} else if extracted {
			cntExtract++
		}
	}
	return cntExtract, nil
}

func extractUpdatedFile(f *zip.File, fpath string) (bool, error) {
	var modifiedExistingFile time.Time
	if exists(fpath) {
		info, err := os.Stat(fpath)
		if err != nil {
			return false, err
		}
		modifiedExistingFile = info.ModTime()
	} else {
		modifiedExistingFile = time.Date(2000, 1, 1, 0, 0, 0, 0, time.Local)
		fDir := filepath.Dir(fpath)
		if !exists(fDir) {
			if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
				return false, err
			}
		}
	}

	if !f.Modified.After(modifiedExistingFile) {
		return false, nil
	}
	outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return false, err
	}
	defer outFile.Close()

	rc, err := f.Open()
	if err != nil {
		return false, err
	}
	defer rc.Close()

	if _, err = io.Copy(outFile, rc); err != nil {
		return false, err
	}

	if err = os.Chtimes(fpath, f.Modified, f.Modified); err != nil {
		return false, err
	}
	fmt.Printf("%v -> %v: %v\n", modifiedExistingFile, f.Modified.In(time.Local), filepath.Base(fpath))
	return true, nil
}

func exists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

func reportError(err error) {
	fmt.Print(err)
	bufio.NewReader(os.Stdin).ReadBytes('\n')
	panic("Error occured")
}

// https://blog.y-yuki.net/entry/2018/08/03/000000
func isWinProcRunning(names ...string) (bool, error) {
	if len(names) == 0 {
		return false, nil
	}

	cmd := exec.Command("tasklist.exe", "/FI", "STATUS eq RUNNING", "/fo", "csv", "/nh")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}

	for _, name := range names {
		if bytes.Contains(out, []byte(name)) {
			return true, nil
		}
	}
	return false, nil
}

func waitTillMBStops() (bool, error) {
	if isRunning, err := isWinProcRunning("MusicBee.exe"); err != nil {
		return false, err
	} else if !isRunning {
		return true, nil
	}
	exec.Command("taskkill", "/im", "MusicBee.exe").Run()
	for {
		<-time.After(time.Second)
		if isRunning, err := isWinProcRunning("MusicBee.exe"); err != nil {
			return false, err
		} else if !isRunning {
			return false, nil
		}
	}
}

func main() {
	var configFilePath string
	flag.Parse()
	if len(flag.Args()) == 1 {
		configFilePath = flag.Args()[0]
	} else {
		configFilePath = "configs.json"
	}
	configs, err := readConfigs(configFilePath)
	if err != nil {
		reportError(err)
	}

	needUpdate, mbSiteTimeStamp, err := needMBUpdate(configs)
	if err != nil {
		reportError(err)
	} else if !needUpdate {
		fmt.Println("No need to download.")
		timeout.Exec(3)
		return
	}

	fmt.Println("Downloading the zip file.")
	downloadPath := configs.MBPatchURL + configs.TargetFileName
	bytesReader, bytesSize, err := downloadFileToMemory(downloadPath)
	if err != nil {
		reportError(err)
	}

	MBNotStarted, err := waitTillMBStops()
	if err != nil {
		reportError(err)
	}
	if updatedCnt, err := extractUpdatedFiles(bytesReader, bytesSize, configs.MBPath); err != nil {
		reportError(err)
	} else if updatedCnt == 0 {
		fmt.Println("All files are up-to-date.")
	} else {
		fmt.Printf("Update/added %v file(s).\n", updatedCnt)
	}

	configs.UpdatedAt = *mbSiteTimeStamp
	if err := writeUpdatedAt(configFilePath, configs); err != nil {
		reportError(err)
	}
	timeout.Exec(7)
	// restart MB
	// https://stackoverflow.com/questions/25633077/how-do-you-add-spaces-to-exec-command-in-golang
	if MBNotStarted {
		return
	}
	cmd := exec.Command(filepath.Join(configs.MBPath, "MusicBee.exe"))
	if err := cmd.Start(); err != nil {
		reportError(err)
	}
}
