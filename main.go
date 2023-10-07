package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gofor-little/env"
	"github.com/sashabaranov/go-openai"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/slides/v1"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	DEBUG          bool
	GOOGLE_API_KEY string
	OPEN_AI_KEY    string
)

type SimpleSlide struct {
	Title   string
	Bullets []string
	Image   string
}

type GPTOutline struct {
	Title  string
	Slides []SimpleSlide
}

func init() {
	var err error

	env.Load("./.env")
	DEBUG = strings.ToLower(env.Get("DEBUG", "false")) == "true"
	GOOGLE_API_KEY = env.Get("GOOGLE_API_KEY", "[NO API KEY]")
	OPEN_AI_KEY, err = env.MustGet("OPEN_AI_KEY")
	if err != nil {
		panic(err)
	}
}

func main() {
	fmt.Println("Here Comes Doctor Slides!")
	args := os.Args
	if len(args) < 2 {
		fmt.Println("I need a document ID to get started, fool.")
		return
	}
	// First arg is the program, second is the ID
	documentId := args[1]
	document := getGoogleDocWithId(documentId)
	textContent := readTextFromDocument(document)
	outline := getGPTOutline(textContent)
	parsedOutline := parseGPTOutline(outline)
	parsedOutline.Title = document.Title
	writeToSlides(parsedOutline)
}

func getGoogleDocWithId(documentId string) *docs.Document {
	ctx := context.Background()
	client := getGoogleClient()
	docsService, err := docs.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		fmt.Println("could not create Google Docs client")
		panic(err)
	}
	doc, err := docsService.Documents.Get(documentId).Do()
	if err != nil {
		fmt.Println("Could not read document")
		panic(err)
	}

	fmt.Printf("Obtained Document: \"%s\"\n", doc.Title)

	return doc
}

func readTextFromDocument(document *docs.Document) string {
	fmt.Println("Reading the text from the document")
	text := ""

	for _, bodyElement := range document.Body.Content {
		paragraph := bodyElement.Paragraph
		if paragraph == nil {
			continue
		}
		paragraphElements := paragraph.Elements
		if paragraphElements == nil {
			continue
		}
		for _, paragraphElement := range paragraphElements {
			textRun := paragraphElement.TextRun
			if textRun == nil {
				continue
			}
			text = text + textRun.Content
		}
	}

	return text
}

func getGPTOutline(content string) string {
	fmt.Println("Asking GPT for a slides outline")
	template := `
	Please use the following document contents in order to build the outline of
	a slideshow. The slideshow must have at least three slides, but can have up
	to 25. Each slide should have a title, at least two content bullet points,
	and a url for an image. The outline should follow thes format for each slide:

	NEW SLIDE ======
	Title: The title of the slide here
	- example bullet point 1
	- example bullet point 2
	- example bullet point 3
	END SLIDE ======

	The document:
	%s`
	message := fmt.Sprintf(template, content)
	client := openai.NewClient(OPEN_AI_KEY)
	resp, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: openai.GPT3Dot5Turbo,
			Messages: []openai.ChatCompletionMessage{
				{
					Role:    openai.ChatMessageRoleUser,
					Content: message,
				},
			},
		},
	)
	if err != nil {
		fmt.Println("Could not ask GPT for help")
		panic(err)
	}

	// There's a possibility this is no good and will crash, but  it is stable
	// enough for now
	responseBody := resp.Choices[0].Message.Content

	return responseBody
}

func parseGPTOutline(outline string) GPTOutline {
	fmt.Println("Trying to make sense of what GPT said...")
	parsedOutline := GPTOutline{}
	parsedOutline.Slides = make([]SimpleSlide, 0)

	var currentSlide SimpleSlide
	lines := strings.Split(outline, "\n")
	for _, line := range lines {
		cleanLine := strings.TrimSpace(line)
		if cleanLine == "NEW SLIDE ======" {
			currentSlide = SimpleSlide{
				Title:   "[UNNAMED]",
				Bullets: make([]string, 0),
			}
		} else if cleanLine == "END SLIDE ======" {
			parsedOutline.Slides = append(parsedOutline.Slides, currentSlide)
		} else if strings.HasPrefix(cleanLine, "Title: ") {
			currentSlide.Title = strings.TrimPrefix(cleanLine, "Title: ")
		} else if strings.HasPrefix(cleanLine, "- ") {
			bullet := strings.TrimPrefix(cleanLine, "- ")
			currentSlide.Bullets = append(currentSlide.Bullets, bullet)
		} else if strings.HasPrefix(cleanLine, "Image URL: ") {
			currentSlide.Image = strings.TrimPrefix(cleanLine, "Image URL: ")
		}
	}

	if len(parsedOutline.Slides) == 0 {
		fmt.Println("Sorry. GPT gave me garbage. I can't do anything with this. Try again?")
		if DEBUG {
			fmt.Println(outline)
		}
		os.Exit(1)
	}

	return parsedOutline
}

func writeToSlides(outline GPTOutline) {
	fmt.Println("Creating your slide show")
	ctx := context.Background()
	client := getGoogleClient()
	slidesService, err := slides.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		panic(err)
	}
	// Creating a slideshow will create an empty sldieshow with a single blank
	// "TITLE" template slide
	presentation := &slides.Presentation{}
	presentation.Title = outline.Title
	presentation, err = slidesService.Presentations.Create(presentation).Do()
	if err != nil {
		panic(err)
	}
	// Now we can add the slides we need based off of the outline. I don't know
	// how to add the content of the slides in the same request as the slide
	// creation so for now we'll just do it in separate pieces.
	updates := slides.BatchUpdatePresentationRequest{}
	updates.Requests = make([]*slides.Request, 0)
	// Each presentation starts with one slide, so we can skip adding a title
	// slide and go straight to the content slides
	for range outline.Slides {
		req := slides.Request{
			CreateSlide: &slides.CreateSlideRequest{
				SlideLayoutReference: &slides.LayoutReference{
					PredefinedLayout: "TITLE_AND_BODY",
				},
			},
		}

		updates.Requests = append(updates.Requests, &req)
	}
	// Add an End Slide to Close Everything Out
	endReq := slides.Request{
		CreateSlide: &slides.CreateSlideRequest{
			SlideLayoutReference: &slides.LayoutReference{
				PredefinedLayout: "TITLE",
			},
		},
	}
	updates.Requests = append(updates.Requests, &endReq)
	// Actually submit the updates
	_, err = slidesService.Presentations.BatchUpdate(presentation.PresentationId, &updates).Do()
	if err != nil {
		panic(err)
	}
	// It's easier to just re-request the presentation to have the up-to-date
	// data for the slideshow than it is to mess with this weird nesting data
	// structure. There's potential for improvements here if I really cared.
	presentation, err = slidesService.Presentations.Get(presentation.PresentationId).Do()
	if err != nil {
		panic(err)
	}
	// No we can start the process of adding all of the desired content in a
	// batched update request
	contentSlidesLength := len(outline.Slides)
	updates = slides.BatchUpdatePresentationRequest{}
	updates.Requests = make([]*slides.Request, 0)
	// Update the title slide
	updates.Requests = append(updates.Requests, &slides.Request{
		InsertText: &slides.InsertTextRequest{
			ObjectId: presentation.Slides[0].PageElements[0].ObjectId,
			Text:     outline.Title,
		},
	})
	// Update the content slides
	for i := 1; i <= contentSlidesLength; i++ {
		slideOutline := outline.Slides[i-1]
		slide := presentation.Slides[i]
		slideParagraph := strings.Join(slideOutline.Bullets, "\n")
		titleAdd := slides.Request{
			InsertText: &slides.InsertTextRequest{
				ObjectId: slide.PageElements[0].ObjectId,
				Text:     slideOutline.Title,
			},
		}
		textAdd := slides.Request{
			InsertText: &slides.InsertTextRequest{
				ObjectId: slide.PageElements[1].ObjectId,
				Text:     slideParagraph,
			},
		}
		updates.Requests = append(updates.Requests, &titleAdd)
		updates.Requests = append(updates.Requests, &textAdd)
	}
	// Update End slide
	updates.Requests = append(updates.Requests, &slides.Request{
		InsertText: &slides.InsertTextRequest{
			ObjectId: presentation.Slides[len(presentation.Slides)-1].PageElements[0].ObjectId,
			Text:     "The End",
		},
	})

	_, err = slidesService.Presentations.BatchUpdate(presentation.PresentationId, &updates).Do()
	if err != nil {
		panic(err)
	}

	fmt.Printf("Created Presentation: https://docs.google.com/presentation/d/%s/edit\n", presentation.PresentationId)
}

func buildBaseSlide() *slides.Page {
	elements := make([]*slides.PageElement, 0)
	slide := slides.Page{
		PageType:     "SLIDE",
		PageElements: elements,
	}

	return &slide
}

func getGoogleClient() *http.Client {
	credsBytes, err := os.ReadFile("./credentials.json")
	if err != nil {
		panic(err)
	}
	config, err := google.ConfigFromJSON(credsBytes, "https://www.googleapis.com/auth/documents", "https://www.googleapis.com/auth/presentations", "https://www.googleapis.com/auth/spreadsheets")
	if err != nil {
		panic(err)
	}
	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokFile, tok)
	}
	return config.Client(context.Background(), tok)
}

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the "+
		"authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		fmt.Println("Unable to read authorization code")
	}

	tok, err := config.Exchange(oauth2.NoContext, authCode)
	if err != nil {
		fmt.Println("Unable to retrieve token from web")
	}

	return tok
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	defer f.Close()
	if err != nil {
		return nil, err
	}
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)

	return tok, err
}

func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	defer f.Close()
	if err != nil {
		fmt.Println("Unable to cache OAuth token")
	}
	json.NewEncoder(f).Encode(token)
}

func runExperiment() {
	f, _ := os.ReadFile("./exampleOutline.txt")
	p := parseGPTOutline(string(f))
	p.Title = fmt.Sprintf("Doctor Slides Test: %s", time.Now())
	writeToSlides(p)
}
