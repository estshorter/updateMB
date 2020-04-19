package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/eiannone/keyboard"
)

func timeout(waitSecond int) {
	ch := make(chan struct{}, 1)
	go func() {
		for i := waitSecond; i > 0; i-- {
			fmt.Printf("\rExit after %d second(s)", i)
			time.Sleep(time.Second)
		}
		fmt.Printf("\rExit after 0 second(s)")
		ch <- struct{}{}
	}()

	go func() {
		_, _, err := keyboard.GetSingleKey()
		if err != nil {
			panic(err)
		}
		ch <- struct{}{}
	}()
	select {
	case <-ch:
		return
	}
}

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
			if !Exists(fpath) {
				os.MkdirAll(fpath, os.ModePerm)
			}
			continue
		}

		extracted, err := extractUpdatedFile(f, fpath)
		if err != nil {
			return cntExtract, err
		}
		if extracted {
			cntExtract++
		}

	}
	return cntExtract, nil
}

func extractUpdatedFile(f *zip.File, fpath string) (bool, error) {
	var modifiedExistingFile time.Time
	if Exists(fpath) {
		info, err := os.Stat(fpath)
		if err != nil {
			return false, err
		}
		modifiedExistingFile = info.ModTime()
	} else {
		modifiedExistingFile = time.Date(2000, 1, 1, 0, 0, 0, 0, time.Local)
		fDir := filepath.Dir(fpath)
		if !Exists(fDir) {
			if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
				return false, err
			}
		}
	}

	if !f.Modified.After(modifiedExistingFile) {
		return false, nil
	}
	outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	defer outFile.Close()
	if err != nil {
		return false, err
	}

	rc, err := f.Open()
	defer rc.Close()
	if err != nil {
		return false, err
	}

	_, err = io.Copy(outFile, rc)
	if err != nil {
		return false, err
	}

	if err = os.Chtimes(fpath, f.Modified, f.Modified); err != nil {
		return false, err
	}
	fmt.Printf("%v -> %v: %v\n", modifiedExistingFile, f.Modified.In(time.Local), filepath.Base(fpath))
	return true, nil
}

// Exists checks if file/directory exists
func Exists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}

func reportError(err error) {
	log.Print(err)
	keyboard.GetSingleKey()
	panic("Error occured")
}

func main() {
	const mbPatchURL = "https://getmusicbee.com/patches/"
	const MBPath = "C:\\Program Files (x86)\\MusicBee\\"
	const cachePath = "C:\\tmp\\"
	const targetFileName = "MusicBee33_Patched.zip"

	lastAccessFileName := cachePath + "mb_last_download_datetime.txt"
	downloadPath := cachePath + targetFileName

	fDir := filepath.Dir(lastAccessFileName)
	if !Exists(fDir) {
		if err := os.MkdirAll(filepath.Dir(lastAccessFileName), os.ModePerm); err != nil {
			reportError(err)
		}
	}

	needUpdate, mbSiteTimeStampStr, err := needMBUpdate(mbPatchURL, targetFileName, lastAccessFileName)
	if err != nil {
		reportError(err)
	}
	if !needUpdate {
		fmt.Println("No need to download.")
		timeout(3)
		return
	}

	fmt.Println("Downloading the zip file.")
	if err := downloadFile(downloadPath, mbPatchURL+targetFileName); err != nil {
		reportError(err)
	}
	exec.Command("taskkill", "/im", "MusicBee.exe").Run()
	updatedCnt, err := extractUpdatedFiles(downloadPath, MBPath)
	if err != nil {
		reportError(err)
	}
	if updatedCnt == 0 {
		fmt.Println("All files are up-to-date.")
	} else {
		fmt.Printf("Update/added %v file(s).\n", updatedCnt)
	}

	ioutil.WriteFile(lastAccessFileName, []byte(mbSiteTimeStampStr), 0644)
	timeout(7)
	// restart MB
	// https://stackoverflow.com/questions/25633077/how-do-you-add-spaces-to-exec-command-in-golang
	cmd := exec.Command(MBPath + "MusicBee.exe")
	if err := cmd.Start(); err != nil {
		reportError(err)
	}
}
