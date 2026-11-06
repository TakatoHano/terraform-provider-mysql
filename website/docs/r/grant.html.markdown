---
layout: "mysql"
page_title: "MySQL: mysql_grant"
sidebar_current: "docs-mysql-resource-grant"
description: |-
  Creates and manages privileges given to a user on a MySQL server
---

# mysql\_grant

The ``mysql_grant`` resource creates and manages privileges given to
a user on a MySQL server.

## Granting Privileges to a User

```hcl
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
```

## Granting Privileges to a Role

```hcl
resource "mysql_role" "developer" {
  name = "developer"
}

resource "mysql_grant" "developer" {
  role       = mysql_role.developer.name
  database   = "app"
  privileges = ["SELECT", "UPDATE"]
}
```

## Adding a Role to a User

```hcl
resource "mysql_user" "jdoe" {
  user               = "jdoe"
  host               = "example.com"
  plaintext_password = "password"
}

resource "mysql_role" "developer" {
  name = "developer"
}

resource "mysql_grant" "developer" {
  user     = mysql_user.jdoe.user
  host     = mysql_user.jdoe.host
  database = "app"
  roles    = [mysql_role.developer.name]
}
```

## Argument Reference

~> **Note:** MySQL removed the `REQUIRE` option from `GRANT` in version 8. `tls_option` is ignored in MySQL 8 and above.

~> **Note:** Attributes `role` and `roles` are only supported in MySQL 8 and above.

The following arguments are supported:

* `user` - (Optional) The name of the user. Conflicts with `role`.
* `host` - (Optional) The source host of the user. Defaults to "localhost". Conflicts with `role`.
* `role` - (Optional) The role to grant `privileges` to. Conflicts with `user` and `host`.
* `database` - (Required) The database to grant privileges on. The name is quoted automatically, so surrounding backticks are optional; wildcards such as `app\_%` are supported.
* `table` - (Optional) Which table to grant `privileges` on. Defaults to `*`, which is all tables. The name is quoted automatically; backticks are optional.
* `privileges` - (Optional) A list of privileges to grant to the user. Refer to a list of privileges (such as [here](https://dev.mysql.com/doc/refman/5.5/en/grant.html)) for applicable privileges. Conflicts with `roles`.
* `roles` - (Optional) A list of roles to grant to the user. Conflicts with `privileges`.
* `tls_option` - (Optional) An TLS-Option for the `GRANT` statement. The value is suffixed to `REQUIRE`. A value of 'SSL' will generate a `GRANT ... REQUIRE SSL` statement. See the [MYSQL `GRANT` documentation](https://dev.mysql.com/doc/refman/5.7/en/grant.html) for more. Ignored if MySQL version is under 5.7.0. Must be `NONE`, `SSL`, `X509`, or one or more of `CIPHER '...'`, `ISSUER '...'`, `SUBJECT '...'` joined by `AND`.
* `grant` - (Optional) Whether to also give the user privileges to grant the same privileges to other users.

## Attributes Reference

No further attributes are exported.

## Drift Detection

On every refresh the provider runs `SHOW GRANTS` for the user or role and compares the result with the configuration. `privileges` (or `roles`) and `grant` are read back, so a privilege that was added or revoked outside Terraform is detected.

~> **Note:** `privileges`, `roles` and `grant` cannot be changed in place. Detected drift therefore plans a **replacement** of the grant (revoke, then grant again). If the user or role no longer has any grant on the configured `database` and `table`, the resource is removed from state and planned for creation.

* `ALL` and `ALL PRIVILEGES` are equivalent; MySQL always reports `ALL PRIVILEGES`, and existing configurations that use `ALL` keep a clean plan. Privilege names are compared case-insensitively.
* Because the privileges of a `database`/`table` pair are read back as a whole, do not declare two `mysql_grant` resources for the same user (or role), database and table; each would see the other's privileges as drift.
* For `roles`, only the configured roles are checked: roles granted to the user by other means are ignored, and configured roles that are no longer granted are reported as drift.
* On MySQL 8, `ALL` on `*.*` is listed by the server as every individual privilege; the provider treats that as `ALL PRIVILEGES`.

## Import

Privileges can be imported using user, host, and database, or role and database, e.g.

```
$ terraform import mysql_grant.jdoe jdoe@example.com:app
$ terraform import mysql_grant.developer developer:app
```

The import reads `table`, `privileges` and `grant` (`WITH GRANT OPTION`) from `SHOW GRANTS`. If the grantee has several table-level grants on the database, the database-wide one (`table = "*"`) is preferred. If the user has no privileges on the database but has been granted roles (MySQL 8), those roles are imported into `roles` instead. Role names that contain `@` cannot be imported.
