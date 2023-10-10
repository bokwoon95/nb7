package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/blugelabs/bluge"
)

var cwd string = func() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Dir(filename)
}()

func main() {
	err := os.RemoveAll(filepath.Join(cwd, "index"))
	if err != nil {
		log.Fatal(err)
	}

	// open an index writer using the configuration
	writer, err := bluge.OpenWriter(bluge.DefaultConfig(filepath.Join(cwd, "index")))
	if err != nil {
		log.Fatalf("error opening writer: %v", err)
	}
	defer func() {
		err = writer.Close()
		if err != nil {
			log.Fatalf("error closing writer: %v", err)
		}
	}()

	// create a document to put in the index
	// the document has one text field named 'name', with value 'bluge'
	doc := bluge.NewDocument("a").
		AddField(bluge.NewTextField("name", "bluge"))

	// update the index with this document
	err = writer.Update(doc.ID(), doc)
	if err != nil {
		log.Fatalf("error updating document: %v", err)
	}
	fmt.Println("indexed document with id:a name:bluge")

	// get a reader for the index
	reader, err := writer.Reader()
	if err != nil {
		log.Fatalf("error getting index reader: %v", err)
	}
	defer func() {
		err = reader.Close()
		if err != nil {
			log.Fatalf("error closing reader: %v", err)
		}
	}()

	// build a query to find the document we indexed
	query := bluge.NewMatchQuery("bluge").SetField("name")

	// build a top-n request to find the top 10 matches,
	// also include the standard aggregations
	request := bluge.NewTopNSearch(10, query).
		WithStandardAggregations()

	fmt.Println("searching for name:bluge")

	// execute this search on the reader
	documentMatchIterator, err := reader.Search(context.Background(), request)
	if err != nil {
		log.Fatalf("error executing search: %v", err)
	}

	// iterate through the document matches
	match, err := documentMatchIterator.Next()
	for err == nil && match != nil {

		// load the identifier for this match
		err = match.VisitStoredFields(func(field string, value []byte) bool {
			if field == "_id" {
				fmt.Printf("match: %s\n", string(value))
			}
			return true
		})
		if err != nil {
			log.Fatalf("error loading stored fields: %v", err)
		}

		// load the next document match
		match, err = documentMatchIterator.Next()
	}
	if err != nil {
		log.Fatalf("error iterator document matches: %v", err)
	}
}
