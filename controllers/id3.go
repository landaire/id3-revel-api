package controllers

import (
	"bitbucket.org/kardianos/osext"
	"bluefish/app/models"
	"bytes"
	"crypto/sha1"
	"errors"
	"fmt"
	"github.com/knieriem/markdown"
	"github.com/kr/binarydist"
	"github.com/kr/s3"
	tag "github.com/landr0id/go-taglib"
	"github.com/robfig/revel"
	"github.com/whatupdave/s3/s3util"
	"io"
	"io/ioutil"
	"net/http"
	u "net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"time"
)

type Id3 struct {
	GorpController
}

const bucketUrl string = "https://s3.amazonaws.com/songdiff"

// GET /id3/
func (c Id3) Index() revel.Result {
	SetActiveItem("")
	return c.Render()
}

// GET /id3/about/
func (c Id3) About() revel.Result {
	SetActiveItem("")
	file, err := os.Open(path.Join(revel.BasePath, "app/views/Id3/about.md"))
	if err != nil {
		return c.RenderError(err)
	}
	defer file.Close()
	parser := markdown.NewParser(nil)
	var buf bytes.Buffer
	fHTML := markdown.ToHTML(&buf)
	parser.Markdown(file, fHTML)

	markdownData := string(buf.Bytes())
	return c.Render(markdownData)
}

// Fixes the ID3 tag info for a remote audio file
// GET /id3/fix
func (c Id3) Fix(url, artist, title string) revel.Result {
	s3util.DefaultConfig.AccessKey = "AKIAJR55H5PX3ME3ZVHA"
	s3util.DefaultConfig.SecretKey = "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"

	// Check the response header to make sure the file is actually an audio file
	resp, err := http.Head(url)
	if match, _ := regexp.MatchString(`unsupported protocol scheme ""`, fmt.Sprintf("%v", err)); match {
		if revel.DevMode {
			resp, err = http.Head("http://127.0.0.1:9000" + url)
		} else {
			resp, err = http.Head("http://127.0.0.1" + url)
		}
	}
	// Check the err
	if err != nil {
		return c.RenderError(err)
	}
	if resp == nil {
		return c.RenderError(errors.New("Invalid response"))
	}
	if resp.StatusCode != 200 {
		return c.RenderError(errors.New("Response status code was not 200 - OK. Got: " + resp.Status))
	}

	// 20 MB
	if resp.ContentLength > 20971520 {
		return c.RenderError(errors.New("File too large"))
	}

	// Get the file
	resp, err = http.Get(url)
	if match, _ := regexp.MatchString(`unsupported protocol scheme ""`, fmt.Sprintf("%v", err)); match {
		if revel.DevMode {
			resp, err = http.Get("http://127.0.0.1:9000" + url)
		} else {
			resp, err = http.Get("http://127.0.0.1" + url)
		}
	}
	// Check the err
	if err != nil {
		return c.RenderError(err)
	}
	if resp == nil {
		return c.RenderError(errors.New("Invalid response"))
	}
	if resp.StatusCode != 200 {
		return c.RenderError(errors.New("Response status code was not 200 - OK. Got: " + resp.Status))
	}
	defer resp.Body.Close()
	// Make sure we were given an audio file by checking the content type
	if match, _ := regexp.MatchString(`audio\.+`, resp.Header.Get("Content-Type")); match {
		return c.RenderError(errors.New("Not an audio file"))
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return c.RenderError(err)
	}

	location, err := u.Parse(url)
	if err != nil {
		return c.RenderError(err)
	}
	// Create a hash of the URL (without the parameters)
	sha := sha1.New()
	revel.TRACE.Println(location.Host + location.Path)
	sha.Write([]byte(location.Host + location.Path))
	diffName := fmt.Sprintf("%X.diff", sha.Sum(nil))

	c.Response.Status = http.StatusOK
	c.Response.ContentType = "audio/mpeg"

	if checkIfDiffExists(diffName, c) {
		revel.TRACE.Println("Diff exists -- using that")
		// Write the body to a buffer, then get rid of the body
		oldbuf := bytes.NewBuffer(body)
		var newbuf bytes.Buffer
		var finalbuf bytes.Buffer
		revel.TRACE.Println(newbuf.Len())

		diffReader, err := getDiff(diffName)
		if err != nil {
			return c.RenderError(err)
		}
		defer diffReader.Close()
		err = binarydist.Patch(oldbuf, &newbuf, diffReader)
		finalbuf.Write(newbuf.Bytes())
		//finalbuf.Write(body[newbuf.Len():len(body)])
		if err != nil {
			return c.RenderError(err)
		}
		revel.TRACE.Printf("%+v", c.Response.Out.Header())
		return &revel.BinaryResult{
			Reader:   &newbuf,
			Name:     fmt.Sprintf("\"%s - %s.mp3\"", artist, title),
			Delivery: "attachment",
			Length:   int64(newbuf.Len()),
			ModTime:  time.Now(),
		}
	} else {
		revel.TRACE.Println("Diff does not exist. Creating new one.")
		result, err := createNewDiff(c, diffName, artist, title, body)
		if err == nil {
			revel.TRACE.Println("adding to database")
			addDiffInfoToDatabase(diffName, artist, title, url, c)
		}
		return result
	}
	return c.RenderError(errors.New(strconv.FormatBool(checkIfDiffExists(diffName, c))))
}

// checkIfDiffExists checks the SQLite3 DB (songs.db) to see if the diff has already been created, and exists
// on the server
func checkIfDiffExists(name string, c Id3) bool {
	// Run query to see if a row in the DB with the name exists
	result, err := c.Txn.Select(&models.DiffPatch{}, `SELECT * FROM DiffPatch where Name = ?`, name)
	// There will be an error returned if the query is fucked or something idk
	if err != nil {
		revel.TRACE.Println(err)
		return false
	}
	// gorp.Transaction.Select returns an array for the result, so if the array is of length 0 then
	// that means that we didn't get jack shit
	if result != nil && len(result) != 0 {
		// Cast the first index from the result array as a DiffPatch object
		x := result[0].(*models.DiffPatch)
		// Increase the request count on the object
		x.RequestCount++
		// Update the model (really just updating the request count)
		c.Txn.Update(x)
		return true
	}
	return false
}

// getDiff returns an io.ReadCloser after opening the file from the S3 bucket
func getDiff(name string) (io.ReadCloser, error) {
	// Opens the object on the bucket server
	r, err := s3util.Open(fmt.Sprintf("%s/%s", bucketUrl, name), nil)
	if err != nil {
		revel.TRACE.Println(err)
		return nil, err
	}
	return r, nil
}

// uploadDiff uploads the diff patch to the S3 bucket
func uploadDiff(name string, data []byte) error {
	// Creates the object on the bucket server
	w, err := s3util.Create(fmt.Sprintf("%s/%s", bucketUrl, name), nil, nil)
	if err != nil {
		revel.TRACE.Println(err)
		return err
	}
	defer w.Close()
	// Write out the patch data
	w.Write(data)
	return nil
}

// addDiffInfoToDatabse takes the relevant info, stores it in the SQLite3 DB
// returns an error if gorp.Transaction.Insert returns an error
func addDiffInfoToDatabase(name, artist, title, url string, c Id3) error {
	// This will only be the DATE, no time.
	dateAdded := time.Now()
	diff := &models.DiffPatch{0, name, url, artist, title, dateAdded.Format("2006-01-02"), dateAdded, 1}
	// Insert the row into the DB
	if err := c.Txn.Insert(diff); err != nil {
		return err
	}
	return nil
}

/* makeRequest is not really needed now, however it will remain until I'm for sure that I don't need to make
*  manual requests
 */
func makeRequest(path, method string, content io.Reader, contentLength int64) *http.Request {
	revel.TRACE.Println(path)
	r, _ := http.NewRequest(method, path, content)
	r.ContentLength = contentLength
	r.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))
	r.Header.Set("X-Amz-Acl", "public-read")
	s3.Sign(r, *s3util.DefaultConfig.Keys)
	return r
}

func createNewDiff(c Id3, diffName, artist, title string, data []byte) (revel.Result, error) {
	filename, _ := osext.ExecutableFolder()

	// Great, let's write this data out to a file
	tempName := filename + "tmp_" + generateRandomString() + ".mp3"
	file, err := os.Create(tempName)
	if err != nil {
		revel.TRACE.Println("Error creating file:", err)
		return c.RenderError(err), err
	}
	defer os.Remove(tempName)
	file.Write(data)
	file.Close()

	// Now that the data is written to a file, do some taglib stuff
	tFile, err := tag.Read(tempName)
	if err != nil {
		fmt.Printf("Error reading file %s: %v\n", tempName, err)
		return c.RenderError(err), err
	}
	if tFile == nil {
		revel.TRACE.Println("Nil file")
		return c.RenderError(err), err
	}
	songTitle := tFile.Title()
	songArtist := tFile.Artist()
	// Set the data if they don't exist
	if songTitle == "" || songArtist == "" {
		tFile.SetTitle(title)
		tFile.SetArtist(artist)
		tFile.Save()
	}
	file.Close()

	// Now do the diff stuff
	file, _ = os.Open(tempName)
	// The info is required for the mod time
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return c.RenderError(err), err
	}
	// oldbuf holds the original data
	var oldbuf bytes.Buffer
	// patch holds the patch data
	var patch bytes.Buffer

	// Make sure that we free the memory
	defer oldbuf.Reset()
	defer patch.Reset()

	// Write out the body data (what was given in the http.Response) to the original
	oldbuf.Write(data)
	// Clear the original data -- we don't need it
	data = nil

	// Generate the patch
	binarydist.Diff(&oldbuf, file, &patch)

	// Upload the diff to the S3 bucket
	err = uploadDiff(diffName, patch.Bytes())
	if err != nil {
		file.Close()
		return c.RenderError(err), err
	}

	// Seek for good measure
	file.Seek(0, os.SEEK_SET)

	return &revel.BinaryResult{
		Reader:   file,
		Name:     fmt.Sprintf("\"%s - %s.mp3\"", artist, title),
		Delivery: "attachment",
		Length:   -1,
		ModTime:  info.ModTime(),
	}, nil
}

func generateRandomString() string {
	utime := time.Now().Unix()
	buf := []byte{
		byte(utime >> 56),
		byte((utime & 0x00FF000000000000) >> 48),
		byte((utime & 0x0000FF0000000000) >> 40),
		byte((utime & 0x000000FF00000000) >> 32),
		byte((utime & 0x00000000FF000000) >> 24),
		byte((utime & 0x0000000000FF0000) >> 16),
		byte((utime & 0x000000000000FF00) >> 8),
		byte(utime & 0x00000000000000FF),
	}
	hash := sha1.New()
	hash.Write(buf)
	return fmt.Sprintf("%X", hash.Sum(nil))
}
