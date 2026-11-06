resource "mysql_user" "jdoe" {
  user               = "jdoe"
  host               = "example.com"
  plaintext_password = "password"
}

# With an authentication plugin
resource "mysql_user" "nologin" {
  user        = "nologin"
  host        = "example.com"
  auth_plugin = "mysql_no_login"
}
