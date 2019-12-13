package youtube

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/iawia002/annie/config"
	"github.com/iawia002/annie/downloader"
	"github.com/iawia002/annie/extractors"
	"github.com/iawia002/annie/request"
	"github.com/iawia002/annie/utils"
)

type args struct {
	PlayerResponse string `json:"player_response"`
	Stream         string `json:"adaptive_fmts"`
	// not every page has `adaptive_fmts` field https://youtu.be/DNaOZovrSVo
	Stream2 string `json:"url_encoded_fmt_stream_map"`
}

type assets struct {
	JS string `json:"js"`
}

type youtubeData struct {
	Args   args   `json:"args"`
	Assets assets `json:"assets"`
}

type youtubeStream struct {
	Itag             int    `json:"itag"`
	URL              string `json:"url"`
	Cipher			 string `json:"cipher"`
	MimeType         string `json:"mimeType"`
	Bitrate          int    `json:"bitrate"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	LastModified     string `json:"lastModified"`
	ContentLength    string `json:"contentLength"`
	Quality          string `json:"quality"`
	QualityLabel     string `json:"qualityLabel"`
	ProjectionType   string `json:"projectionType"`
	AverageBitrate   int    `json:"averageBitrate"`
	AudioQuality     string `json:"audioQuality"`
	ApproxDurationMs string `json:"approxDurationMs"`
	AudioSampleRate  string `json:"audioSampleRate"`
	AudioChannels    int    `json:"audioChannels"`
}

const referer = "https://www.youtube.com"

// Extract is the main function for extracting data
func Extract(uri string) ([]downloader.Data, error) {
	utils.PrintVerbose("youtube.Extract(): begin parse: %s\n", uri)
	var err error
	if !config.Playlist {
		return []downloader.Data{youtubeDownload(uri)}, nil
	}
	listIDs := utils.MatchOneOf(uri, `(list|p)=([^/&]+)`)
	if listIDs == nil || len(listIDs) < 3 {
		return nil, extractors.ErrURLParseFailed
	}
	listID := listIDs[2]
	if len(listID) == 0 {
		return nil, errors.New("can't get list ID from URL")
	}

	utils.PrintVerbose("youtube.Extract(): begin fetch playlist id: %s\n", listID)
	html, err := request.Get("https://www.youtube.com/playlist?list="+listID, referer, nil)
	if err != nil {
		return nil, err
	}
	// "videoId":"OQxX8zgyzuM","thumbnail"
	videoIDs := utils.MatchAll(html, `"videoId":"([^,]+?)","thumbnail"`)
	needDownloadItems := utils.NeedDownloadList(len(videoIDs))
	extractedData := make([]downloader.Data, len(needDownloadItems))
	wgp := utils.NewWaitGroupPool(config.ThreadNumber)
	dataIndex := 0
	for index, videoID := range videoIDs {
		if !utils.ItemInSlice(index+1, needDownloadItems) || len(videoID) < 2 {
			continue
		}
		u := fmt.Sprintf(
			"https://www.youtube.com/watch?v=%s&list=%s", videoID[1], listID,
		)
		wgp.Add()
		go func(index int, u string, extractedData []downloader.Data) {
			defer wgp.Done()
			extractedData[index] = youtubeDownload(u)
		}(dataIndex, u, extractedData)
		dataIndex++
	}
	wgp.Wait()
	return extractedData, nil
}

// youtubeDownload download function for single url
func youtubeDownload(uri string) downloader.Data {
	var err error
	vid := utils.MatchOneOf(
		uri,
		`watch\?v=([^/&]+)`,
		`youtu\.be/([^?/]+)`,
		`embed/([^/?]+)`,
		`v/([^/?]+)`,
	)
	if vid == nil || len(vid) < 2 {
		return downloader.EmptyData(uri, errors.New("can't find vid"))
	}

	videoURL := fmt.Sprintf(
		"https://www.youtube.com/watch?v=%s",
		vid[1],
	)
	utils.PrintVerbose("youtube.youtubeDownload(): begin fetch HTML %s\n", videoURL)
	html, err := request.Get(videoURL, referer, nil)
	if err != nil {
		return downloader.EmptyData(uri, err)
	}
	ytplayer := utils.MatchOneOf(html, `;ytplayer\.config\s*=\s*({.+?});`)
	if ytplayer == nil || len(ytplayer) < 2 {
		if strings.Contains(html, "LOGIN_REQUIRED") ||
			strings.Contains(html, "Sign in to confirm your age") {
			return downloader.EmptyData(uri, extractors.ErrLoginRequired)
		} else {
			return downloader.EmptyData(uri, extractors.ErrURLParseFailed)
		}
	}

	if config.ExtractedData {
		//fmt.Printf("ytplayer[1]: %s\n", ytplayer[1])
	}

	var youtube youtubeData
	if err = json.Unmarshal([]byte(ytplayer[1]), &youtube); err != nil {
		return downloader.EmptyData(uri, err)
	}
	title := utils.GetStringFromJson(youtube.Args.PlayerResponse, "videoDetails.title")

	if config.ExtractedData {
		fmt.Printf("get title: %s\n", title)
		fmt.Printf("get Stream: %#v\n", youtube.Args.Stream)
		fmt.Printf("get Stream2: %#v\n", youtube.Args.Stream2)
		fmt.Printf("get PlayerResponse: %#v\n", youtube.Args.PlayerResponse)
	}

	streams, err := extractVideoURLS(youtube, uri)
	if err != nil {
		return downloader.EmptyData(uri, err)
	}

	return downloader.Data{
		Site:    "YouTube youtube.com",
		Title:   title,
		Type:    "video",
		Streams: streams,
		URL:     uri,
	}
}

func extractVideoURLS(data youtubeData, referer string) (map[string]downloader.Stream, error) {
	utils.PrintVerbose("youtube.extractVideoURLS(): begin\n")
	var youtubeStreams []string

	if config.YouTubeStream2 || data.Args.Stream == "" {
		youtubeStreams = strings.Split(data.Args.Stream2, ",")
	} else {
		youtubeStreams = strings.Split(data.Args.Stream, ",")
	}
	var ext string
	var audio downloader.URL
	streams := map[string]downloader.Stream{}

	maybeStreamData := utils.GetArrayFromJson(data.Args.PlayerResponse, "streamingData.adaptiveFormats")
	if len(maybeStreamData) == 0 {
		// fall back to small formats
		maybeStreamData = utils.GetArrayFromJson(data.Args.PlayerResponse, "streamingData.formats")
	}

	if config.ExtractedData {
		fmt.Printf("youtubeStreams: %#v\n", youtubeStreams)
		fmt.Printf("maybeStreamData: %#v\n", maybeStreamData)
	}

	if len(maybeStreamData) != 0 && data.Args.Stream == "" {
		for _, sd := range maybeStreamData {
			var s youtubeStream
			if err := json.Unmarshal([]byte(sd.String()), &s); err != nil {
				return nil, fmt.Errorf("parse alt stream failed: %w", err)
			}
			itag := strconv.Itoa(s.Itag)
			isAudio := strings.HasPrefix(s.MimeType, "audio/mp4")
			quality := s.QualityLabel
			if quality == "" {
				quality = s.Quality
			}
			if quality != "" {
				quality = fmt.Sprintf("%s %s", quality, s.MimeType)
			} else {
				quality = s.MimeType
			}
			if isAudio {
				// audio file use m4a extension
				ext = "m4a"
			} else {
				exts := utils.MatchOneOf(s.MimeType, `(\w+)/(\w+);`)
				if exts == nil || len(exts) < 3 {
					return nil, extractors.ErrURLParseFailed
				}
				ext = exts[2]
			}

			var e error
			realURL := s.URL
			if realURL == "" && s.Cipher != "" {
				streamQuery, err := url.ParseQuery(s.Cipher)
				if err != nil {
					return nil, err
				}
				realURL, e = getDownloadURL(streamQuery, data.Assets.JS)
				if e != nil {
					return nil, e
				}
			}
			cl, _ := strconv.Atoi(s.ContentLength)
			urlData := downloader.URL{
				URL:  realURL,
				Size: int64(cl),
				Ext:  ext,
			}
			if isAudio {
				// Audio data for merging with video
				audio = urlData
			}
			streams[itag] = downloader.Stream{
				URLs:    []downloader.URL{urlData},
				Size:    int64(cl),
				Quality: quality,
			}
			utils.PrintVerbose("youtube.extractVideoURLS(): get quality: %s\n", quality)
		}
	} else {
		err, done := proccessNormalStream(youtubeStreams, ext, data, referer, audio, streams)
		if done {
			return streams, err
		}
	}

	// Unlike `url_encoded_fmt_stream_map`, all videos in `adaptive_fmts` have no sound,
	// we need download video and audio both and then merge them.
	// Another problem is that even if we add `ratebypass=yes`, the download speed still slow sometimes. https://github.com/iawia002/annie/issues/191#issuecomment-405449649

	// All videos here have no sound and need to be added separately
	for itag, f := range streams {
		if strings.Contains(f.Quality, "video/") {
			f.Size += audio.Size
			f.URLs = append(f.URLs, audio)
			streams[itag] = f
		}
	}
	return streams, nil
}

func proccessNormalStream(youtubeStreams []string, ext string, data youtubeData, referer string, audio downloader.URL, streams map[string]downloader.Stream) (error, bool) {
	for _, s := range youtubeStreams {
		if s == "" {
			//skip empty stream URL
			continue
			//return nil, fmt.Errorf("empty stream URL")
		}
		if config.Debug {
			fmt.Printf("extractVideoURLS(): begin parse stream: %s\n", s)
		}
		stream, err := url.ParseQuery(s)
		if err != nil {
			return err, true
		}
		itag := stream.Get("itag")
		streamType := stream.Get("type")
		isAudio := strings.HasPrefix(streamType, "audio/mp4")

		quality := stream.Get("quality_label")
		if quality == "" {
			quality = stream.Get("quality") // for url_encoded_fmt_stream_map
		}
		if quality != "" {
			quality = fmt.Sprintf("%s %s", quality, streamType)
		} else {
			quality = streamType
		}
		if isAudio {
			// audio file use m4a extension
			ext = "m4a"
		} else {
			exts := utils.MatchOneOf(streamType, `(\w+)/(\w+);`)
			if exts == nil || len(exts) < 3 {
				return extractors.ErrURLParseFailed, true
			}
			ext = exts[2]
		}
		realURL, err := getDownloadURL(stream, data.Assets.JS)
		if err != nil {
			return err, true
		}
		size, err := request.Size(realURL, referer)
		if err != nil {
			// some stream of the video will return a 404 error,
			// I don't know if it is a problem with the signature algorithm.
			// https://github.com/iawia002/annie/issues/322
			continue
		}
		urlData := downloader.URL{
			URL:  realURL,
			Size: size,
			Ext:  ext,
		}
		if isAudio {
			// Audio data for merging with video
			audio = urlData
		}
		streams[itag] = downloader.Stream{
			URLs:    []downloader.URL{urlData},
			Size:    size,
			Quality: quality,
		}
		utils.PrintVerbose("youtube.extractVideoURLS(): get quality: %s\n", quality)
	}

	// `url_encoded_fmt_stream_map`
	if data.Args.Stream == "" {
		return nil, true
	}
	return nil, false
}
