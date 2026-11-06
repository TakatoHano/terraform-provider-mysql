# Create a database server
resource "aws_db_instance" "default" {
  engine         = "mysql"
  engine_version = "5.6.17"
  instance_class = "db.t1.micro"
  name           = "initial_db"
  username       = "rootuser"
  password       = "rootpasswd"

  # etc, etc; see aws_db_instance docs for more
}

# Configure the MySQL provider based on the outcome of
# creating the aws_db_instance.
provider "mysql" {
  endpoint = aws_db_instance.default.endpoint
  username = aws_db_instance.default.username
  password = aws_db_instance.default.password
}

# Create a second database, in addition to the "initial_db" created
# by the aws_db_instance resource above.
resource "mysql_database" "app" {
  name = "another_db"
}
