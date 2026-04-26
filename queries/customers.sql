-- name: CreateCustomer :one
INSERT INTO customers (id, name, email, birth_date, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateCustomer :one
UPDATE customers
SET name       = $2,
    email      = $3,
    birth_date = $4,
    updated_at = $5
WHERE id = $1
RETURNING *;

-- name: DeleteCustomer :exec
DELETE FROM customers WHERE id = $1;

-- name: GetCustomerByID :one
SELECT * FROM customers WHERE id = $1;

-- name: GetCustomerByEmail :one
SELECT * FROM customers WHERE email = $1;

-- name: ListCustomers :many
SELECT * FROM customers ORDER BY created_at DESC;
