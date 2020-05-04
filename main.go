package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
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

func getLastAccessDate(path string) string {
	content, err := ioutil.ReadFile(path)
	if err != nil {
		return "2020-01-01 00:00"
	}
	return string(content)
}

func needMBUpdate(mbPatchURL, targetFileName, lastAccessFileName string) (bool, string, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET", mbPatchURL, nil)
	if err != nil {
		return false, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return false, "", err
	}

	mbSiteTimeStampRaw := doc.Find("a[href=\"" + targetFileName + "\"]").Parent().Next().Text()
	mbSiteTimeStampStr := strings.TrimSpace(mbSiteTimeStampRaw)
	mbSiteTimeStamp, err := time.Parse("2006-01-02 15:04", mbSiteTimeStampStr)
	if err != nil {
		return false, "", err
	}
	lastAccessDate, err := time.Parse("2006-01-02 15:04", getLastAccessDate(lastAccessFileName))
	if err != nil {
		return false, "", err
	}
	if mbSiteTimeStamp.After(lastAccessDate) {
		return true, mbSiteTimeStampStr, nil
	}
	return false, mbSiteTimeStampStr, nil
}

func downloadFile(filepath, url string) error {
	// Get the data
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Create the file
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	return err
}

func extractUpdatedFiles(src, dest string) (int, error) {
	cntExtract := 0

	r, err := zip.OpenReader(src)
	if err != nil {
		return cntExtract, err
	}
	defer r.Close()

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

//https://blog.y-yuki.net/entry/2018/08/03/000000
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

func waitTillMBStops() error {
	if isRunning, err := isWinProcRunning("MusicBee.exe"); err != nil {
		return err
	} else if !isRunning {
		return nil
	}
	exec.Command("taskkill", "/im", "MusicBee.exe").Run()
	for {
		select {
		case <-time.After(time.Second):
			if isRunning, err := isWinProcRunning("MusicBee.exe"); err != nil {
				return err
			} else if !isRunning {
				return nil
			}
		}
	}

}

func main() {
	const mbPatchURL = "https://getmusicbee.com/patches/"
	const MBPath = "C:/Program Files (x86)/MusicBee/"
	const cachePath = "C:/tmp"
	const targetFileName = "MusicBee33_Patched.zip"

	lastAccessFileName := filepath.Join(cachePath, "mb_last_download_datetime.txt")
	downloadPath := filepath.Join(cachePath, targetFileName)

	if !exists(filepath.Clean(cachePath)) {
		if err := os.MkdirAll(filepath.Dir(lastAccessFileName), os.ModePerm); err != nil {
			reportError(err)
		}
	}

	needUpdate, mbSiteTimeStampStr, err := needMBUpdate(mbPatchURL, targetFileName, lastAccessFileName)
	if err != nil {
		reportError(err)
	} else if !needUpdate {
		fmt.Println("No need to download.")
		timeout.Exec(3)
		return
	}

	fmt.Println("Downloading the zip file.")
	if err := downloadFile(downloadPath, mbPatchURL+targetFileName); err != nil {
		reportError(err)
	}

	if err := waitTillMBStops(); err != nil {
		reportError(err)
	}
	if updatedCnt, err := extractUpdatedFiles(downloadPath, MBPath); err != nil {
		reportError(err)
	} else if updatedCnt == 0 {
		fmt.Println("All files are up-to-date.")
	} else {
		fmt.Printf("Update/added %v file(s).\n", updatedCnt)
	}

	ioutil.WriteFile(lastAccessFileName, []byte(mbSiteTimeStampStr), 0644)
	timeout.Exec(7)
	// restart MB
	// https://stackoverflow.com/questions/25633077/how-do-you-add-spaces-to-exec-command-in-golang
	cmd := exec.Command(filepath.Join(MBPath, "MusicBee.exe"))
	if err := cmd.Start(); err != nil {
		reportError(err)
	}
}
