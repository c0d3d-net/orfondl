package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type MPD struct {
	XMLName     xml.Name `xml:"MPD"`
	PublishTime string   `xml:"publishTime,attr"`
	Periods     []Period `xml:"Period"`
}

type Period struct {
	AdaptationSet []AdaptationSet `xml:"AdaptationSet"`
}

type AdaptationSet struct {
	Representation  []Representation  `xml:"Representation"`
	SegmentTemplate []SegmentTemplate `xml:"SegmentTemplate"`
}

type Representation struct {
	ID                string `xml:"id,attr"`
	Width             int    `xml:"width,attr"`
	Height            int    `xml:"height,attr"`
	AudioSamplingRate int    `xml:"audioSamplingRate,attr"`
	Codec             string `xml:"codec,attr"`
	Bandwidth         int    `xml:"bandwidth,attr"`
}

type SegmentTemplate struct {
	Timescale       int             `xml:"timescale,attr"`
	Media           string          `xml:"media,attr"`
	Initialization  string          `xml:"initialization,attr"`
	SegmentTimeline SegmentTimeline `xml:"SegmentTimeline"`
}

type SegmentTimeline struct {
	S []S `xml:"S"`
}

type S struct {
	T int `xml:"t,attr"`
	D int `xml:"d,attr"`
	R int `xml:"r,attr"`
}

func downloadAndAppendFile(url, filePath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download file: %v", err)
	}
	defer resp.Body.Close()

	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return fmt.Errorf("failed to append to file: %v", err)
	}

	return nil
}

func writeStreamToFile(baseUrl string, templates SegmentTemplate, representation Representation, timeline []S, file string) {
	initFile := strings.Replace(templates.Initialization, "$RepresentationID$", representation.ID, 1)
	fmt.Println("Downloading segment", initFile)
	if err := downloadAndAppendFile(baseUrl+initFile, file); err != nil {
		fmt.Println("Error:", err)
		return
	}

	time := 0
	for _, segment := range timeline {
		mediaFile := strings.Replace(templates.Media, "$RepresentationID$", representation.ID, 1)
		mediaFile = strings.Replace(mediaFile, "$Time$", strconv.Itoa(time), 1)
		fmt.Println("Downloading segment", mediaFile)
		if err := downloadAndAppendFile(baseUrl+mediaFile, file); err != nil {
			fmt.Println("Error:", err)
			return
		}
		time += segment.D

		if segment.R > 0 {
			for i := 0; i < segment.R; i++ {
				mediaFile = strings.Replace(templates.Media, "$RepresentationID$", representation.ID, 1)
				mediaFile = strings.Replace(mediaFile, "$Time$", strconv.Itoa(time), 1)
				fmt.Println("Downloading segment", mediaFile)
				if err := downloadAndAppendFile(baseUrl+mediaFile, file); err != nil {
					fmt.Println("Error:", err)
					return
				}
				time += segment.D
			}
		}
	}
}

func mergeVideoAndAudio(videoFile, audioFile, output string) error {
	fmt.Printf("Merging %s and %s into %s\n", videoFile, audioFile, output)

	cmd := exec.Command("ffmpeg", "-loglevel", "error", "-y", "-i", videoFile, "-i", audioFile, "-c", "copy", output)
	err := cmd.Run()

	if err != nil {
		return fmt.Errorf("ffmpeg error: %v", err)
	}

	if err := os.Remove(videoFile); err != nil {
		fmt.Printf("Warning: Failed to delete video file %s: %v\n", videoFile, err)
	}
	if err := os.Remove(audioFile); err != nil {
		fmt.Printf("Warning: Failed to delete audio file %s: %v\n", audioFile, err)
	}

	fmt.Println("Done merging video and audio")
	return nil
}

func extractUrls(text string) []string {
	var urls []string
	regex := regexp.MustCompile(`"(https?:\/\/[^"\s]+?manifest\.mpd)"`)
	matches := regex.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) > 1 {
			urls = append(urls, match[1])
		}
	}
	return urls
}

func downloadVideo(url string, output string) {
	response, err := http.Get(url)
	if err != nil {
		fmt.Println("Could not fetch video page")
		os.Exit(-1)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		fmt.Println("Could not fetch video page")
		os.Exit(-1)
	}

	htmlBytes, err := ioutil.ReadAll(response.Body)
	if err != nil {
		fmt.Println("Failed to read response body")
		os.Exit(-1)
	}
	html := string(htmlBytes)

	titleRegex := regexp.MustCompile(`<title>(.*?)<\/title>`)
	titleMatch := titleRegex.FindStringSubmatch(html)
	var title string
	if len(titleMatch) > 1 {
		title = titleMatch[1]
		title = strings.ReplaceAll(title, ":", " -") // FIX
	}

	urls := extractUrls(html)
	if len(urls) == 0 {
		fmt.Println("Could not find video manifest.mpd")
		os.Exit(-1)
	}

	var manifestUrl string
	for _, url := range urls {
		if manifestUrl == "" {
			manifestUrl = url
			continue
		}
		if strings.Contains(url, "QXB.mp4") {
			manifestUrl = url
			break
		}
	}

	fmt.Printf("Title: %s\nManifest URL: %s\n", title, manifestUrl)

	xmlResponse, err := http.Get(manifestUrl)
	if err != nil || xmlResponse.StatusCode != http.StatusOK {
		fmt.Println("Could not fetch manifest.mpd")
		os.Exit(-1)
	}
	defer xmlResponse.Body.Close()

	xmlBytes, err := ioutil.ReadAll(xmlResponse.Body)
	if err != nil {
		fmt.Println("Failed to read XML response body")
		os.Exit(-1)
	}

	var manifest MPD
	err = xml.Unmarshal(xmlBytes, &manifest)
	if err != nil {
		fmt.Printf("Error parsing XML: %v\n", err)
		os.Exit(-1)
	}

	if output == "" {
		output = title + ".mp4"
	}

	output = strings.ReplaceAll(output, ":", " -") // FIX

	fmt.Printf("Output file: %s\n", output)

	videoSet := manifest.Periods[0].AdaptationSet[0]
	var highestVideo Representation
	for _, rep := range videoSet.Representation {
		if highestVideo.Width < rep.Width {
			highestVideo = rep
		}
	}
	videoTemplates := videoSet.SegmentTemplate[0]
	videoTimeline := videoSet.SegmentTemplate[0].SegmentTimeline.S

	audioSet := manifest.Periods[0].AdaptationSet[1]
	var highestAudio Representation
	for _, rep := range audioSet.Representation {
		if highestAudio.AudioSamplingRate < rep.AudioSamplingRate {
			highestAudio = rep
		}
	}
	audioTemplates := audioSet.SegmentTemplate[0]
	audioTimeline := audioSet.SegmentTemplate[0].SegmentTimeline.S

	fmt.Printf("Video:\n")
	fmt.Printf("ID: %s, Width: %d, Height: %d, Codec: %s, Bandwidth: %d\n",
		highestVideo.ID, highestVideo.Width, highestVideo.Height, highestVideo.Codec, highestVideo.Bandwidth)
	fmt.Printf("Video Template:\nMedia: %s, Initialization: %s\n", videoTemplates.Media, videoTemplates.Initialization)
	fmt.Printf("Audio:\n")
	fmt.Printf("ID: %s, AudioSamplingRate: %d, Codec: %s, Bandwidth: %d\n",
		highestAudio.ID, highestAudio.AudioSamplingRate, highestAudio.Codec, highestAudio.Bandwidth)
	fmt.Printf("Audio Template:\nMedia: %s, Initialization: %s\n", audioTemplates.Media, audioTemplates.Initialization)

	baseUrl := strings.Replace(manifestUrl, "/manifest.mpd", "/", -1)
	videoFile := "__" + output + ".video"
	audioFile := "__" + output + ".audio"

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		writeStreamToFile(baseUrl, videoTemplates, highestVideo, videoTimeline, videoFile)
	}()

	go func() {
		defer wg.Done()
		writeStreamToFile(baseUrl, audioTemplates, highestAudio, audioTimeline, audioFile)
	}()

	wg.Wait()

	if err := mergeVideoAndAudio(videoFile, audioFile, output); err != nil {
		fmt.Printf("Error merging video and audio: %v\n", err)
	} else {
		fmt.Println("Merging completed successfully.")
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("orfondl <video-url> or orfondl linklist.txt")
		os.Exit(-1)
	}
	urlOrFile := os.Args[1]
	var output string
	if len(os.Args) > 2 {
		output = os.Args[2]
		output = strings.ReplaceAll(output, ":", " -") // FIX
	}

	if strings.HasPrefix(urlOrFile, "http") {
		downloadVideo(urlOrFile, output)
	} else {
		content, err := ioutil.ReadFile(urlOrFile)
		if err != nil {
			fmt.Printf("Error reading file: %v\n", err)
			os.Exit(-1)
		}
		urls := strings.Split(string(content), "\n")
		for _, url := range urls {
			downloadVideo(url, "")
		}
	}
}
