package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sashabaranov/go-openai"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/docs/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/slides/v1"
	"github.com/gofor-little/env"
	"net/http"
	"os"
	"strings"
)

var (
	DEBUG bool
	GOOGLE_API_KEY string
	OPEN_AI_KEY string
)

type SimpleSlide struct {
	Title   string
	Bullets []string
	Image   string
}

type GPTOutline struct {
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
	fmt.Println("Welcome to Doctor Slides!")
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
	a slideshow. The slideshow must have at least three slides. Each slide
	should have a title, at least two content bullet points, and a url for an image. The outline
	should follow thes format for each slide:

	NEW SLIDE ======
	Title: The title of the slide here
	- example bullet point 1
	- example bullet point 2
	- example bullet point 3
	END SLIDE ======

	The document:
	%s\n`
	message := fmt.Sprintf(template, content)
	if DEBUG {
		fmt.Printf(template, "[YOUR DOCUMENT HERE]")
	}
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
	presentationId := "1EAYk18WDjIG-zp_0vLm3CsfQh_i8eXc67Jo2O9C6Vuc"
	presentation, err := slidesService.Presentations.Get(presentationId).Do()
	if err != nil {
		panic(err)
	}
	fmt.Println(presentation.Title)
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
