package main

import "fmt"

func main() {
	messages := make(chan string, 1)
	messages <- "hello"
	messages <- "world"
	fmt.Println(<-messages)
	fmt.Println(<-messages)
}
