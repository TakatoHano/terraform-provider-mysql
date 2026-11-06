resource "mysql_user" "jdoe" {
  user = "jdoe"
}

resource "mysql_user_password" "jdoe" {
  user    = mysql_user.jdoe.user
  pgp_key = "keybase:joestump"
}
