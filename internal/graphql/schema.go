// Package graphql serves the part of GitHub's GraphQL API that finished MCP
// tools call. Every resolver forwards the caller's token to Gitea.
package graphql

// schemaText deliberately omits custom issue fields and issue parents. Queries
// that select them fail validation, and the MCP server falls back or reports it.
const schemaText = `
schema {
	query: Query
}

scalar DateTime
scalar BigInt

enum IssueState { OPEN CLOSED }
enum IssueOrderField { CREATED_AT UPDATED_AT COMMENTS }
enum OrderDirection { ASC DESC }

input IssueOrder {
	field: IssueOrderField!
	direction: OrderDirection!
}

input IssueFilters {
	since: DateTime
}

type Query {
	repository(owner: String!, name: String!): Repository
}

type Repository {
	isPrivate: Boolean!
	issue(number: Int!): Issue
	issues(
		first: Int
		after: String
		labels: [String!]
		states: [IssueState!]
		orderBy: IssueOrder
		filterBy: IssueFilters
	): IssueConnection!
}

type IssueConnection {
	nodes: [Issue!]!
	pageInfo: PageInfo!
	totalCount: Int!
}

type PageInfo {
	hasNextPage: Boolean!
	hasPreviousPage: Boolean!
	startCursor: String
	endCursor: String
}

type Issue {
	number: Int!
	title: String!
	body: String!
	state: IssueState!
	databaseId: BigInt
	author: Actor
	createdAt: DateTime!
	updatedAt: DateTime!
	labels(first: Int!): LabelConnection!
	assignees(first: Int!): UserConnection!
	comments: IssueCommentConnection!
}

type Actor {
	login: String!
}

type User {
	login: String!
}

type UserConnection {
	nodes: [User!]!
}

type Label {
	id: ID!
	name: String!
	color: String!
	description: String
}

type LabelConnection {
	nodes: [Label!]!
	totalCount: Int!
}

type IssueCommentConnection {
	totalCount: Int!
}
`
