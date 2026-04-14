package utils

import "crypto/sha256"
import _ "os"
import "fmt"
import "strings"
import "encoding/hex"

func VerifyProofOfWork(data string, nonce string, difficulty int, allowedDigits string) bool {
	input := data + nonce
	hash := sha256.Sum256([]byte(input))
	hashStr := hex.EncodeToString(hash[:])

	fmt.Printf("verifyProofOfWork: data = %s, nonce=%s, difficulty=%d, allowedDigits=%s\n",
			   data, nonce, difficulty, allowedDigits)

	for i := 0; i < difficulty; i++ {
		if !strings.Contains(allowedDigits, hashStr[i:i+1]) {
			return false
		}
	}
	return true
}

/*
func main() {
    // e.g. go run pow.go 55038811-0f9e-44f5-b2c7-47e285d4b90a 918737
    domain := os.Args[1]
    nonce := os.Args[2]
    fmt.Printf("domain = %s, nonce=%s, %+v\n", domain, nonce, verifyProofOfWork(domain, nonce, 17))
}
*/
