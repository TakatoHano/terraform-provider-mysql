# Import using user, host and database
terraform import mysql_grant.jdoe jdoe@example.com:app

# Import using role and database
terraform import mysql_grant.developer developer:app
