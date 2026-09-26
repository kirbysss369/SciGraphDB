package openalex

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type Work struct {
	ID              string
	DOI             string
	Title           string
	Abstract        *string
	PublicationYear *int
	CitedByCount    int
	Topics          []Topic
	ReferencedWorks []string
}

type Topic struct {
	ID    string  `json:"id"`
	Name  string  `json:"display_name"`
	Score float64 `json:"score"`
}

type responsePage struct {
	Results []rawWork `json:"results"`
	Meta    struct {
		NextCursor *string `json:"next_cursor"`
	} `json:"meta"`
}

type rawWork struct {
	ID                    string           `json:"id"`
	DOI                   *string          `json:"doi"`
	DisplayName           string           `json:"display_name"`
	Title                 string           `json:"title"`
	PublicationYear       *int             `json:"publication_year"`
	CitedByCount          int              `json:"cited_by_count"`
	AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
	Topics                []Topic          `json:"topics"`
	ReferencedWorks       []string         `json:"referenced_works"`
}

func decodePage(data []byte) (responsePage, error) {
	var page responsePage
	if err := json.Unmarshal(data, &page); err != nil {
		return responsePage{}, errors.New("invalid OpenAlex JSON response")
	}
	if page.Results == nil {
		return responsePage{}, errors.New("OpenAlex response has no results array")
	}
	return page, nil
}

func (raw rawWork) work() (Work, error) {
	abstract, err := reconstructAbstract(raw.AbstractInvertedIndex)
	if err != nil {
		return Work{}, err
	}
	title := raw.DisplayName
	if title == "" {
		title = raw.Title
	}
	w := Work{
		ID: raw.ID, Title: title, Abstract: abstract,
		PublicationYear: raw.PublicationYear, CitedByCount: raw.CitedByCount,
		Topics: raw.Topics, ReferencedWorks: raw.ReferencedWorks,
	}
	if raw.DOI != nil {
		w.DOI = *raw.DOI
	}
	return w, nil
}

func reconstructAbstract(index map[string][]int) (*string, error) {
	if index == nil {
		return nil, nil
	}
	type token struct {
		position int
		word     string
	}
	var tokens []token
	for word, positions := range index {
		for _, position := range positions {
			if position < 0 {
				return nil, errors.New("OpenAlex abstract has a negative position")
			}
			tokens = append(tokens, token{position, word})
		}
	}
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].position == tokens[j].position {
			return tokens[i].word < tokens[j].word
		}
		return tokens[i].position < tokens[j].position
	})
	words := make([]string, 0, len(tokens))
	for _, item := range tokens {
		words = append(words, item.word)
	}
	text := strings.Join(words, " ")
	return &text, nil
}
