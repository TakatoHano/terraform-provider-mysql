# Granting privileges to a user
resource "mysql_user" "jdoe" {
  user               = "jdoe"
  host               = "example.com"
  plaintext_password = "password"
}

resource "mysql_grant" "jdoe" {
  user       = mysql_user.jdoe.user
  host       = mysql_user.jdoe.host
  database   = "app"
  privileges = ["SELECT", "UPDATE"]
}

# Granting privileges to a role (MySQL 8 and above)
resource "mysql_role" "developer" {
  name = "developer"
}

resource "mysql_grant" "developer" {
  role       = mysql_role.developer.name
  database   = "app"
  privileges = ["SELECT", "UPDATE"]
}

# Adding a role to a user (MySQL 8 and above)
resource "mysql_grant" "jdoe_developer" {
  user     = mysql_user.jdoe.user
  host     = mysql_user.jdoe.host
  database = "app"
  roles    = [mysql_role.developer.name]
}
