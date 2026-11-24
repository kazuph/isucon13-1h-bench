package main
import("database/sql";"fmt";"log";_ "github.com/go-sql-driver/mysql")
func main(){db,e:=sql.Open("mysql","isucon:isucon@tcp(127.0.0.1:3306)/isupipe");if e!=nil{log.Fatal(e)};for _,p:=range [][2]string{{"A","a"},{"あ","ア"},{"é","e"},{"SS","ß"},{"a_b","_"},{"a%b","%"},{"a\\b","\\"}}{var col string;var match int;if e:=db.QueryRow("SELECT COLLATION(?), ? LIKE CONCAT('%', ?, '%')",p[0],p[0],p[1]).Scan(&col,&match);e!=nil{log.Fatal(e)};fmt.Printf("%q %q %s %d\n",p[0],p[1],col,match)}}
