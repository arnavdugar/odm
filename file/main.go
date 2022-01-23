package main

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"unicode/utf16"
)

var filename = flag.String("f", "", ".odm file")
var outputDirectory = flag.String("o", ".", "output directory")

const ClientId = "00000000-0000-0000-0000-000000000000"
const HashSecret = "ELOSNOC*AIDEM*EVIRDREVO"
const OMC = "1.2.0"
const OS = "10.14.2"
const UserAgent = "OverDrive Media Console"

type OverDriveMedia struct {
	AcquisitionUrl Url      `xml:"License>AcquisitionUrl"`
	ContentId      string   `xml:"id,attr"`
	Formats        []Format `xml:"Formats>Format"`
}

type Format struct {
	Parts     Parts      `xml:"Parts"`
	Protocols []Protocol `xml:"Protocols>Protocol"`
}

type Protocol struct {
	Method  string `xml:"method,attr"`
	BaseUrl string `xml:"baseurl,attr"`
}

type Parts struct {
	Count int    `xml:"count,attr"`
	Part  []Part `xml:"Part"`
}

type Part struct {
	Filename string `xml:"filename,attr"`
	Name     string `xml:"name,attr"`
	Number   uint   `xml:"number,attr"`
}

type Url struct {
	Value *url.URL
}

func (u *Url) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var value string
	err := d.DecodeElement(&value, &start)
	if err != nil {
		return err
	}

	u.Value, err = url.Parse(value)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	flag.Parse()

	err := run()
	if err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if *filename == "" {
		return errors.New("odm file required")
	}

	licenseValue := fmt.Sprintf("%s|%s|%s|%s", ClientId, OMC, OS, HashSecret)
	encodedLicenseValue := utf16.Encode([]rune(licenseValue))

	hash := sha1.New()
	binary.Write(hash, binary.LittleEndian, encodedLicenseValue)

	licenseHash := hash.Sum(nil)
	encodedLicenseHash := base64.StdEncoding.EncodeToString(licenseHash)

	file, err := os.Open(*filename)
	if err != nil {
		return err
	}
	defer file.Close()

	odmDecoder := xml.NewDecoder(file)
	data := OverDriveMedia{}
	err = odmDecoder.Decode(&data)
	if err != nil {
		return err
	}

	if len(data.Formats) != 1 {
		return fmt.Errorf("expected 1 format, got %d", len(data.Formats))
	}

	if len(data.Formats[0].Parts.Part) != data.Formats[0].Parts.Count {
		return fmt.Errorf("expected %d format, got %d",
			data.Formats[0].Parts.Count, len(data.Formats))
	}

	if len(data.Formats[0].Protocols) != 1 {
		return fmt.Errorf("expected 1 protocol, got %d",
			len(data.Formats[0].Protocols))
	}

	if data.Formats[0].Protocols[0].Method != "download" {
		return fmt.Errorf("unknown protocol method: %s",
			data.Formats[0].Protocols[0].Method)
	}

	acquisitionUrl := data.AcquisitionUrl.Value
	acquisitionUrl.RawQuery = url.Values{
		"MediaID":  []string{data.ContentId},
		"ClientID": []string{ClientId},
		"OMC":      []string{OMC},
		"OS":       []string{OS},
		"Hash":     []string{encodedLicenseHash},
	}.Encode()

	request, err := http.NewRequest("GET", acquisitionUrl.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", UserAgent)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}

	if response.StatusCode != http.StatusOK {
		responseBody, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("acquiring license returned a %d status: %s",
			response.StatusCode, responseBody)
	}

	license, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}

	for _, part := range data.Formats[0].Parts.Part {
		err := downloadFile(
			data.Formats[0].Protocols[0].BaseUrl, part, string(license))
		if err != nil {
			return err
		}
	}

	return nil
}

func downloadFile(baseUrl string, part Part, license string) error {
	partUrl := fmt.Sprintf("%s/%s", baseUrl, part.Filename)
	request, err := http.NewRequest("GET", partUrl, nil)
	if err != nil {
		return err
	}

	request.Header.Set("ClientId", ClientId)
	request.Header.Set("License", license)
	request.Header.Set("User-Agent", UserAgent)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf(
			"downloading file returned a %d status", response.StatusCode)
	}

	filePath := path.Join(*outputDirectory, fmt.Sprintf("%s.mp3", part.Name))
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
