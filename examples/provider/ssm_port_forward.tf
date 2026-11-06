# Create VPC and Subnets
module "vpc" {

  # (sample value)
  public_subnets   = ["10.99.0.0/24", "10.99.1.0/24", "10.99.2.0/24"]
  private_subnets  = ["10.99.3.0/24", "10.99.4.0/24", "10.99.5.0/24"]
  database_subnets = ["10.99.7.0/24", "10.99.8.0/24", "10.99.9.0/24"]

  create_database_subnet_group = true
  enable_nat_gateway           = true
  single_nat_gateway           = true

  # etc, etc; see vpc module docs for more
}

# Create a database server
resource "aws_db_instance" "default" {
  # attach database_subnet (private)
  db_subnet_group_name = module.vpc.database_subnet_group_name

  # etc, etc; see aws_db_instance docs for more
}

# Create a server for bastion. (installed SSM Agent)
resource "aws_instance" "bastion" {
  # attach i am role (based on AmazonSSMMManagedInstanceCore Policy)
  iam_instance_profile = resource.aws_iam_role.ssm_role.name
  # attach private_subnet
  subnet_id = module.vpc.private_subnets[0]
  # etc, etc; see aws_instance docs for more
}

# Configure the MySQL provider based on the outcome of
# creating the aws_db_instance.
provider "mysql" {
  # endpoint's host must be localhost, and port is unused.
  endpoint = "localhost:${unused_port}"
  username = aws_db_instance.default.username
  password = aws_db_instance.default.password

  aws_ssm_session_manager_client_config {
    # No SSH keys or sshd are needed in this mode.
    mode            = "port_forwarding"
    ec2_instance_id = resource.aws_instance.bastion.id
    rds_endpoint    = resource.aws_db_instance.default.endpoint
    aws_profile     = local.aws_profile
    region          = local.region
  }
}
