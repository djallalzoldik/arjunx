package main

import (
    "bufio"
    "bytes"
    "flag"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "os"
    "strings"
    "sync"
    "time"

    "github.com/PuerkitoBio/goquery"
)

const (
    maxWorkers = 10
)

func main() {
    // Define flags for custom headers, proxy, output file, concurrency, timeout, quiet mode, verbose mode, follow redirects,
    // request method, base URL, input file, and error log.
    customHeaders := make(headerFlag, 0)
    flag.Var(&customHeaders, "H", "Custom header in the form 'key: value'")
    proxyFlag := flag.String("proxy", "", "HTTP proxy in the form 'http://127.0.0.1:8080'")
    outputFlag := flag.String("o", "extracted_params.txt", "Output file for extracted parameters")
    concurrencyFlag := flag.Int("c", 10, "Number of concurrent workers")
    timeoutFlag := flag.Duration("t", time.Second*30, "HTTP request timeout")
    quietFlag := flag.Bool("q", false, "Quiet mode (suppress non-error output)")
    verboseFlag := flag.Bool("v", false, "Verbose mode (print detailed information)")
    followRedirectsFlag := flag.Bool("r", true, "Follow HTTP redirects")
    methodFlag := flag.String("method", "GET", "HTTP request method")
    baseURLFlag := flag.String("baseurl", "", "Base URL to prepend to input URLs")
    inputFlag := flag.String("i", "", "Input file containing URLs to process")
    errorLogFlag := flag.String("e", "", "Error log file")

    scanner := bufio.NewScanner(os.Stdin)

    if *inputFlag != "" {
        // Read URLs from the specified input file
        inputFile, err := os.Open(*inputFlag)
        if err != nil {
            fmt.Printf("Error opening input file: %v\n", err)
            return
        }
        defer inputFile.Close()
        scanner = bufio.NewScanner(inputFile)
    }

    file, err := os.Create(*outputFlag)
    if err != nil {
        fmt.Printf("Error creating file: %v\n", err)
        return
    }
    defer file.Close()

    errorLogFile := os.Stderr
    if *errorLogFlag != "" {
        // Open the specified error log file
        errorLogFile, err = os.Create(*errorLogFlag)
        if err != nil {
            fmt.Printf("Error creating error log file: %v\n", err)
            return
        }
        defer errorLogFile.Close()
    }

    urlCh := make(chan string, *concurrencyFlag)
    var wg sync.WaitGroup

    // Create workers
    for i := 0; i < *concurrencyFlag; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for u := range urlCh {
                extractParamsFromURL(u, file, customHeaders, *proxyFlag, *timeoutFlag, *quietFlag, *verboseFlag, *followRedirectsFlag, *methodFlag, *baseURLFlag)
            }
        }()
    }

    // Parse command-line arguments
    flag.Parse()

    // Read URLs from scanner and send them to the channel
    for scanner.Scan() {
        urlCh <- scanner.Text()
    }
    close(urlCh) // Close the channel to signal workers that no more URLs will be sent

    wg.Wait() // Wait for all workers to finish

    if err := scanner.Err(); err != nil {
        fmt.Fprintln(os.Stderr, "reading standard input:", err)
    }
}

type headerFlag []string

func (h *headerFlag) String() string {
    return fmt.Sprintf("%v", *h)
}

func (h *headerFlag) Set(value string) error {
    *h = append(*h, value)
    return nil
}

func extractParamsFromURL(u string, file *os.File, customHeaders headerFlag, proxy string, timeout time.Duration, quiet, verbose, followRedirects bool, method, baseURL string) {
    resp, err := createRequestWithCustomHeaders(u, customHeaders, proxy, timeout, followRedirects, method, baseURL)
    if err != nil {
        if !quiet {
            fmt.Printf("Error requesting URL %s: %v\n", u, err)
        }
        return
    }
    defer resp.Body.Close()

    buf := bytes.NewBuffer(nil)
    if _, err := io.Copy(buf, resp.Body); err != nil {
        if !quiet {
            fmt.Printf("Error reading response body from URL %s: %v\n", u, err)
        }
        return
    }

    // Extract query parameters from the response body using goquery
    queryParameters := extractQueryParamsFromHTML(buf.String())

    if len(queryParameters) > 0 {
        // Append the extracted query parameters to the original URL
        parsedURL, _ := url.Parse(u)
        parsedURL.RawQuery = queryParameters.Encode()

        // Save the modified URL to the output file
        modifiedURL := parsedURL.String()
        if _, err := file.WriteString(modifiedURL + "\n"); err != nil {
            if !quiet {
                fmt.Printf("Error writing to file: %v\n", err)
            }
        }

        if verbose {
            fmt.Printf("Extracted parameters from URL %s:\n", u)
            for key, values := range queryParameters {
                fmt.Printf("%s: %s\n", key, strings.Join(values, ", "))
            }
        }
    }
}

func createRequestWithCustomHeaders(u string, customHeaders headerFlag, proxy string, timeout time.Duration, followRedirects bool, method, baseURL string) (*http.Response, error) {
    client := &http.Client{
        Timeout: timeout,
    }

    // Create a custom HTTP transport if a proxy is specified
    if proxy != "" {
        proxyURL, err := url.Parse(proxy)
        if err != nil {
            return nil, err
        }
        transport := &http.Transport{
            Proxy: http.ProxyURL(proxyURL),
        }
        client.Transport = transport
    }

    // Construct the final URL by prepending the base URL (if provided)
    finalURL := u
    if baseURL != "" {
        finalURL = baseURL + u
    }

    req, err := http.NewRequest(method, finalURL, nil)
    if err != nil {
        return nil, err
    }

    // Set the FollowRedirects option
    if !followRedirects {
        client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
            return http.ErrUseLastResponse
        }
    }

    // Add custom headers to the request
    for _, header := range customHeaders {
        parts := strings.SplitN(header, ":", 2)
        if len(parts) != 2 {
            return nil, fmt.Errorf("Invalid header format: %s", header)
        }
        key := strings.TrimSpace(parts[0])
        value := strings.TrimSpace(parts[1])
        req.Header.Add(key, value)
    }

    return client.Do(req)
}

func extractQueryParamsFromHTML(responseBody string) url.Values {
    queryParameters := make(url.Values)

    doc, err := goquery.NewDocumentFromReader(strings.NewReader(responseBody))
    if err == nil {
        // Extract parameters from various HTML elements and attributes
        doc.Find("input, select, textarea, a").Each(func(index int, element *goquery.Selection) {
            // Extract parameters based on element and attribute names
            name := element.AttrOr("name", "")
            value := element.AttrOr("value", "")
            href := element.AttrOr("href", "")
            src := element.AttrOr("src", "")

            // Check if the value is empty or contains only spaces
            if name != "" {
                value = strings.TrimSpace(value)
                if value == "" || strings.TrimSpace(value) == "" {
                    value = "FUZZ"
                }
                queryParameters[name] = append(queryParameters[name], value)
            }

            // Extract parameters from href or src attributes
            if href != "" {
                parsedURL, _ := url.Parse(href)
                queryParameters = mergeQueryParameters(queryParameters, parsedURL.Query())
            }
            if src != "" {
                parsedURL, _ := url.Parse(src)
                queryParameters = mergeQueryParameters(queryParameters, parsedURL.Query())
            }
        })

        // Ensure that all empty or space-only parameter values are formed with "FUZZ"
        for key := range queryParameters {
            for i := range queryParameters[key] {
                queryParameters[key][i] = strings.TrimSpace(queryParameters[key][i])
                if queryParameters[key][i] == "" || strings.TrimSpace(queryParameters[key][i]) == "" {
                    queryParameters[key][i] = "FUZZ"
                }
            }
        }
    }

    return queryParameters
}


// Helper function to merge query parameters from two maps
func mergeQueryParameters(params1, params2 url.Values) url.Values {
    for key, values := range params2 {
        params1[key] = append(params1[key], values...)
    }
    return params1
}
