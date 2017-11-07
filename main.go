package main

import (
	"fmt"
	"github.com/PuerkitoBio/goquery"
	gorillaFeeds "github.com/gorilla/feeds"
	"github.com/hashicorp/hcl"
	"github.com/mmcdole/gofeed"
	"html/template"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"bytes"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
)

type PlanetConfig struct {
	Title       string
	Description string
	Author      struct {
		Name  string
		Email string
	}
	S3 struct {
		Bucket       string
		Accessid     string
		AccessSecret string
		Region       string
	}
	MaxPosts  int
	Feeds     []PlanetFeed
	Templates []struct {
		Src   string
		Dest  string
		S3Key string
	}
	DateFormat string
	FeedParser struct {
		Selector string
		Url      string
	}
	RSSOutput struct {
		Dest  string
		S3Key string
	}
}

var HtmlContentType = "text/html"

type PlanetFeed struct {
	Name string
	Url  string
}

type PlanetPost struct {
	*gofeed.Item
	Feed           *gofeed.Feed
	EncodedContent template.HTML
	NewDate        bool
	IsFirst        bool
	Date           string
}

type PlanetData struct {
	Posts []*PlanetPost
	Feeds []*gofeed.Feed
	Title string
}

var config PlanetConfig
var posts []*PlanetPost
var feeds []*gofeed.Feed

func readConfig() {

	rawConfig, err := ioutil.ReadFile("config.hcl")
	if err != nil {
		log.Fatal(err)
	}

	err = hcl.Unmarshal(rawConfig, &config)
	if err != nil {
		log.Fatal(err)
	}

	if config.S3.Bucket == "" {
		config.S3.Bucket = os.Getenv("S3_BUCKET")
	}

	if config.S3.AccessSecret == "" {
		config.S3.AccessSecret = os.Getenv("S3_ACCESS_SECRET")
	}

	if config.S3.Accessid == "" {
		config.S3.Accessid = os.Getenv("S3_ACCESS_ID")
	}

	if config.S3.Region == "" {
		config.S3.Region = os.Getenv("S3_REGION")
	}

}

func fetchFeeds() {

	parser := gofeed.NewParser()
	for _, feedUrl := range config.Feeds {

		feed, err := parser.ParseURL(feedUrl.Url)
		if err != nil {
			fmt.Errorf("Error Parsing Feed: %v\n", feedUrl)
			continue
		}

		feeds = append(feeds, feed)

		for _, post := range feed.Items {

			if post.Published == "" && post.Updated == "" {
				continue
			} else if post.Published == "" && post.Updated != "" {
				post.Published = post.Updated
				post.PublishedParsed = post.UpdatedParsed
			} else if post.Updated == "" && post.Published != "" {
				post.Updated = post.Published
				post.UpdatedParsed = post.PublishedParsed
			}

			item := &PlanetPost{Item: post, Feed: feed, EncodedContent: template.HTML(post.Content)}
			item.Date = post.PublishedParsed.Format(config.DateFormat)

			for _, encoded := range post.Extensions["content"]["encoded"] {
				if encoded.Name == "encoded" {
					item.EncodedContent = template.HTML(encoded.Value)
				}
			}

			posts = append(posts, item)
		}

	}

	sort.Sort(ByPublished(posts))
}

func getFeedsFromSelector(url, selector string) {
	doc, err := goquery.NewDocument(url)
	if err != nil {
		log.Fatal(err)
	}

	doc.Find(selector).Each(func(i int, s *goquery.Selection) {
		feed := []rune(s.Text())

		if feed[0] == '[' && feed[len(feed)-1] == ']' {
			feed = feed[1 : len(feed)-1]
		}

		config.Feeds = append(config.Feeds, PlanetFeed{"", string(feed)})

	})

}

func init() {

}

func s3Upload(Key, Content, ContentType string) error {
	sess, err := session.NewSession(&aws.Config{Region: &config.S3.Region, Credentials: credentials.NewStaticCredentials(config.S3.Accessid, config.S3.AccessSecret, "")})
	if err != nil {
		return err
	}

	svc := s3.New(sess)

	if ContentType == "" {
		ContentType = HtmlContentType
	}

	input := &s3.PutObjectInput{
		Body:        strings.NewReader(Content),
		Bucket:      &config.S3.Bucket,
		Key:         &Key,
		ContentType: &ContentType,
	}

	_, err = svc.PutObject(input)
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			default:
				fmt.Println(aerr.Error())
			}
		} else {
			fmt.Println(err.Error())
		}
		return err
	}

	return nil
}

func main() {
	posts = make([]*PlanetPost, 0)
	feeds = make([]*gofeed.Feed, 0)
	readConfig()

	if config.FeedParser.Url != "" {
		getFeedsFromSelector(config.FeedParser.Url, config.FeedParser.Selector)
	}

	fetchFeeds()

	curDate := ""

	for i, post := range posts {
		if i == 0 {
			post.NewDate = true
			post.IsFirst = true
			curDate = post.PublishedParsed.Format(config.DateFormat)
		}

		if curDate != post.PublishedParsed.Format(config.DateFormat) {
			post.NewDate = true
			curDate = post.PublishedParsed.Format(config.DateFormat)
		}

	}

	sort.Sort(ByTitle(feeds))

	if config.MaxPosts > len(posts) {
		config.MaxPosts = len(posts)
	}
	data := PlanetData{Title: config.Title, Posts: posts[:config.MaxPosts], Feeds: feeds}

	for _, tmpl := range config.Templates {
		tmpl_bytes, err := ioutil.ReadFile(tmpl.Src)
		if err != nil {
			log.Fatalln(err)
		}

		t := template.New(tmpl.Src)
		t, err = t.Parse(string(tmpl_bytes[:]))

		if err != nil {
			log.Fatalln(err)
		}

		buf := new(bytes.Buffer)
		t.Execute(buf, data)

		if tmpl.Dest != "" {
			f, err := os.Create(tmpl.Dest)
			if err != nil {
				log.Fatalln(err)
			}
			f.Write(buf.Bytes())
		}

		if tmpl.S3Key != "" {
			s3Upload(tmpl.S3Key, buf.String(), "text/html")
		}

	}

	if config.RSSOutput.Dest != "" || config.RSSOutput.S3Key != "" {
		now := time.Now()

		feed := &gorillaFeeds.Feed{
			Title:       config.Title,
			Description: config.Description,
			Author:      &gorillaFeeds.Author{Name: config.Author.Name, Email: config.Author.Email},
			Created:     now,
			Link:        &gorillaFeeds.Link{Href: ""},
		}

		feed.Items = make([]*gorillaFeeds.Item, 0)

		for _, post := range posts[:config.MaxPosts] {
			item := &gorillaFeeds.Item{
				Source:      &gorillaFeeds.Link{Href: post.Feed.Link},
				Title:       post.Title,
				Link:        &gorillaFeeds.Link{Href: post.Link},
				Description: post.Description,
				Author:      &gorillaFeeds.Author{Name: post.Author.Name, Email: post.Author.Email},
				Created:     *post.PublishedParsed,
			}
			feed.Items = append(feed.Items, item)
		}

		rss, err := feed.ToRss()
		if err != nil {
			log.Fatalln(err)
		}

		if config.RSSOutput.Dest != "" {
			f, err := os.Create(config.RSSOutput.Dest)
			if err != nil {
				log.Fatal(err)
			}
			f.WriteString(rss)
			f.Close()
		}

		if config.RSSOutput.S3Key != "" {
			s3Upload(config.RSSOutput.S3Key, rss, "text/xml")
		}

	}

}

type ByTitle []*gofeed.Feed

func (a ByTitle) Len() int           { return len(a) }
func (a ByTitle) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByTitle) Less(i, j int) bool { return a[i].Title < a[j].Title }

type ByPublished []*PlanetPost

func (a ByPublished) Len() int      { return len(a) }
func (a ByPublished) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a ByPublished) Less(i, j int) bool {
	return a[i].PublishedParsed.Unix() > a[j].PublishedParsed.Unix()
}
