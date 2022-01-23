package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path"
	"regexp"
	"time"

	"golang.org/x/net/html"
)

var overDriveUrl = flag.String("u", "", "url")
var outputDirectory = flag.String("o", ".", "output directory")
var rateInterval = flag.String("i", "2s", "rate interval")
var retryCount = flag.Int("r", 3, "retry count")

var dataRegxp = regexp.MustCompile("window.bData = (?P<Data>{.*})")
var errDownloadFailed = errors.New("download failed")

type PublicSuffixList struct{}

func (PublicSuffixList) PublicSuffix(domain string) string {
	return domain
}

func (PublicSuffixList) String() string {
	return "PublicSuffixList"
}

type HtmlNode struct {
	Tag string
	Id  string
}

type Data struct {
	Spine []Spine `json:"spine"`
}

type Spine struct {
	Path         string `json:"path"`
	OriginalPath string `json:"-odread-original-path"`
}

type DownloadAttempt struct {
	Count   int
	Index   int
	Name    string
	Request *http.Request
}

func main() {
	flag.Parse()

	err := run()
	if err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if *overDriveUrl == "" {
		return errors.New("odm url required")
	}

	interval, err := time.ParseDuration(*rateInterval)
	if err != nil {
		return err
	}

	jar, err := cookiejar.New(&cookiejar.Options{
		PublicSuffixList: PublicSuffixList{},
	})
	if err != nil {
		return err
	}

	client := http.Client{
		Jar: jar,
	}

	request, err := http.NewRequest("GET", *overDriveUrl, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "nobody")

	response, err := client.Do(request)
	if err != nil {
		return err
	}

	if response.StatusCode != http.StatusOK {
		responseBody, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("url returned a %d status: %s",
			response.StatusCode, responseBody)
	}

	document, err := html.Parse(response.Body)
	if err != nil {
		return err
	}

	htmlPath := []HtmlNode{{
		Tag: "html",
	}, {
		Tag: "body",
	}, {
		Tag: "div",
		Id:  "BIFOCAL-runtime",
	}, {
		Tag: "script",
		Id:  "BIFOCAL-data",
	}}

	current := document
pathLoop:
	for _, selector := range htmlPath {
		for node := current.FirstChild; node != nil; node = node.NextSibling {
			if node.Type != html.ElementNode {
				continue
			}
			if node.Data != selector.Tag {
				continue
			}
			if selector.Id == "" {
				current = node
				continue pathLoop
			}
			for _, attribute := range node.Attr {
				if attribute.Key != "id" {
					continue
				}
				if attribute.Val != selector.Id {
					continue
				}
				current = node
				continue pathLoop
			}
		}
		return fmt.Errorf("unable to find element <%s>", selector.Tag)
	}

	dataBytes := []byte(html.UnescapeString(current.FirstChild.Data))
	dataBytes = dataRegxp.FindSubmatch(dataBytes)[1]

	filePath := path.Join(*outputDirectory, "metadata.json")
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.Write(dataBytes)
	if err != nil {
		return err
	}

	data := Data{}
	err = json.Unmarshal(dataBytes, &data)
	if err != nil {
		return err
	}

	requests := make([]DownloadAttempt, len(data.Spine))
	for index, spine := range data.Spine {
		url := fmt.Sprintf("%s://%s/%s",
			response.Request.URL.Scheme, response.Request.URL.Host, spine.Path)
		request, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Referer", response.Request.URL.String())
		request.Header.Set("User-Agent", "nobody")

		requests[index] = DownloadAttempt{
			Count:   0,
			Index:   index + 1,
			Name:    spine.OriginalPath,
			Request: request,
		}
	}

	ticker := time.NewTicker(interval)
	for index := 0; index < len(requests); index += 1 {
		<-ticker.C
		downloadRequest := requests[index]

		log.Printf("downloading file %d\n", downloadRequest.Index)
		err := downloadFile(
			downloadRequest.Index, client, downloadRequest.Request,
			downloadRequest.Name)
		if err == errDownloadFailed {
			if downloadRequest.Count < *retryCount {
				log.Printf(
					"downloading file %d failed; retrying\n", downloadRequest.Index)
				downloadRequest.Count += 1
				requests = append(requests, downloadRequest)
			} else {
				log.Printf("downloading file %d failed\n", downloadRequest.Index)
			}
		} else if err != nil {
			return fmt.Errorf(
				"downloading file %d failed: %v", downloadRequest.Index, err)
		}
	}

	return nil
}

func downloadFile(
	index int, client http.Client, request *http.Request, name string,
) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}

	if response.StatusCode == http.StatusNoContent {
		return errDownloadFailed
	} else if response.StatusCode != http.StatusOK {
		return fmt.Errorf("returned %d status", response.StatusCode)
	}

	filePath := path.Join(*outputDirectory, name)
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, response.Body)
	if err != nil {
		return err
	}

	return nil
}
