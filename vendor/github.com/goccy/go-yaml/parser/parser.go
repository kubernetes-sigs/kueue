package parser

import (
	"fmt"
	"os"
	"strings"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/internal/errors"
	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"
)

type parser struct {
	idx    int
	size   int
	tokens token.Tokens
}

func newParser(tokens token.Tokens, mode Mode) *parser {
	filteredTokens := []*token.Token{}
	if mode&ParseComments != 0 {
		filteredTokens = tokens
	} else {
		for _, tk := range tokens {
			if tk.Type == token.CommentType {
				continue
			}
			// keep prev/next reference between tokens containing comments
			// https://github.com/goccy/go-yaml/issues/254
			filteredTokens = append(filteredTokens, tk)
		}
	}
	return &parser{
		idx:    0,
		size:   len(filteredTokens),
		tokens: token.Tokens(filteredTokens),
	}
}

func (p *parser) next() bool {
	return p.idx < p.size
}

func (p *parser) previousToken() *token.Token {
	if p.idx > 0 {
		return p.tokens[p.idx-1]
	}
	return nil
}

func (p *parser) insertToken(idx int, tk *token.Token) {
	if p.size < idx {
		return
	}
	if p.size == idx {
		curToken := p.tokens[p.size-1]
		tk.Next = curToken
		curToken.Prev = tk

		p.tokens = append(p.tokens, tk)
		p.size = len(p.tokens)
		return
	}

	curToken := p.tokens[idx]
	tk.Next = curToken
	curToken.Prev = tk

	p.tokens = append(p.tokens[:idx+1], p.tokens[idx:]...)
	p.tokens[idx] = tk
	p.size = len(p.tokens)
}

func (p *parser) currentToken() *token.Token {
	if p.idx >= p.size {
		return nil
	}
	return p.tokens[p.idx]
}

func (p *parser) nextToken() *token.Token {
	if p.idx+1 >= p.size {
		return nil
	}
	return p.tokens[p.idx+1]
}

func (p *parser) nextNotCommentToken() *token.Token {
	for i := p.idx + 1; i < p.size; i++ {
		tk := p.tokens[i]
		if tk.Type == token.CommentType {
			continue
		}
		return tk
	}
	return nil
}

func (p *parser) afterNextNotCommentToken() *token.Token {
	notCommentTokenCount := 0
	for i := p.idx + 1; i < p.size; i++ {
		tk := p.tokens[i]
		if tk.Type == token.CommentType {
			continue
		}
		notCommentTokenCount++
		if notCommentTokenCount == 2 {
			return tk
		}
	}
	return nil
}

func (p *parser) isCurrentCommentToken() bool {
	tk := p.currentToken()
	if tk == nil {
		return false
	}
	return tk.Type == token.CommentType
}

func (p *parser) progressIgnoreComment(num int) {
	if p.size <= p.idx+num {
		p.idx = p.size
	} else {
		p.idx += num
	}
}

func (p *parser) progress(num int) {
	if p.isCurrentCommentToken() {
		return
	}
	p.progressIgnoreComment(num)
}

func (p *parser) parseMapping(ctx *context) (*ast.MappingNode, error) {
	mapTk := p.currentToken()
	node := ast.Mapping(mapTk, true)
	node.SetPath(ctx.path)
	p.progress(1) // skip MappingStart token
	for p.next() {
		tk := p.currentToken()
		if tk.Type == token.MappingEndType {
			node.End = tk
			return node, nil
		} else if tk.Type == token.CollectEntryType {
			p.progress(1)
			continue
		}

		value, err := p.parseMappingValue(ctx)
		if err != nil {
			return nil, err
		}
		mvnode, ok := value.(*ast.MappingValueNode)
		if !ok {
			return nil, errors.ErrSyntax("failed to parse flow mapping node", value.GetToken())
		}
		node.Values = append(node.Values, mvnode)
		p.progress(1)
	}
	return nil, errors.ErrSyntax("unterminated flow mapping", node.GetToken())
}

func (p *parser) parseSequence(ctx *context) (*ast.SequenceNode, error) {
	node := ast.Sequence(p.currentToken(), true)
	node.SetPath(ctx.path)
	p.progress(1) // skip SequenceStart token

	isFirst := true
	for p.next() {
		tk := p.currentToken()
		if tk.Type == token.SequenceEndType {
			node.End = tk
			break
		} else if tk.Type == token.CollectEntryType {
			p.progress(1)
		} else if !isFirst {
			return nil, errors.ErrSyntax("',' or ']' must be specified", tk)
		}

		if tk := p.currentToken(); tk != nil && tk.Type == token.SequenceEndType {
			// this case is here: "[ elem, ]".
			// In this case, ignore the last element and break sequence parsing.
			node.End = tk
			break
		}

		value, err := p.parseToken(ctx.withIndex(uint(len(node.Values))), p.currentToken())
		if err != nil {
			return nil, err
		}
		node.Values = append(node.Values, value)
		p.progress(1)
		isFirst = false
	}
	if node.End == nil || node.End.Type != token.SequenceEndType {
		return nil, errors.ErrSyntax("sequence end token ']' not found", node.Start)
	}
	return node, nil
}

func (p *parser) parseTag(ctx *context) (*ast.TagNode, error) {
	tagToken := p.currentToken()
	node := ast.Tag(tagToken)
	node.SetPath(ctx.path)
	p.progress(1) // skip tag token
	var (
		value ast.Node
		err   error
	)
	switch token.ReservedTagKeyword(tagToken.Value) {
	case token.MappingTag,
		token.OrderedMapTag:
		value, err = p.parseMapping(ctx)
	case token.IntegerTag,
		token.FloatTag,
		token.StringTag,
		token.BinaryTag,
		token.TimestampTag,
		token.NullTag:
		typ := p.currentToken().Type
		if typ == token.LiteralType || typ == token.FoldedType {
			value, err = p.parseLiteral(ctx)
		} else {
			value = p.parseScalarValue(p.currentToken())
		}
	case token.SequenceTag,
		token.SetTag:
		err = errors.ErrSyntax(fmt.Sprintf("sorry, currently not supported %s tag", tagToken.Value), tagToken)
	default:
		// custom tag
		value, err = p.parseToken(ctx, p.currentToken())
	}
	if err != nil {
		return nil, err
	}
	node.Value = value
	return node, nil
}

func (p *parser) removeLeftSideNewLineCharacter(src string) string {
	// CR or LF or CRLF
	return strings.TrimLeft(strings.TrimLeft(strings.TrimLeft(src, "\r"), "\n"), "\r\n")
}

func (p *parser) existsNewLineCharacter(src string) bool {
	if strings.Index(src, "\n") > 0 {
		return true
	}
	if strings.Index(src, "\r") > 0 {
		return true
	}
	return false
}

func (p *parser) validateMapKey(tk *token.Token) error {
	if tk.Type != token.StringType {
		return nil
	}
	origin := p.removeLeftSideNewLineCharacter(tk.Origin)
	if p.existsNewLineCharacter(origin) {
		return errors.ErrSyntax("unexpected key name", tk)
	}
	return nil
}

func (p *parser) createNullToken(base *token.Token) *token.Token {
	pos := *(base.Position)
	pos.Column++
	return token.New("null", "null", &pos)
}

func (p *parser) parseMapValue(ctx *context, key ast.MapKeyNode, colonToken *token.Token) (ast.Node, error) {
	node, err := p.createMapValueNode(ctx, key, colonToken)
	if err != nil {
		return nil, err
	}
	if node != nil && node.GetPath() == "" {
		node.SetPath(ctx.path)
	}
	return node, nil
}

func (p *parser) createMapValueNode(ctx *context, key ast.MapKeyNode, colonToken *token.Token) (ast.Node, error) {
	tk := p.currentToken()
	if tk == nil {
		nullToken := p.createNullToken(colonToken)
		p.insertToken(p.idx, nullToken)
		return ast.Null(nullToken), nil
	} else if tk.Type == token.CollectEntryType {
		// implicit null value.
		return ast.Null(tk), nil
	}
	var comment *ast.CommentGroupNode
	if tk.Type == token.CommentType {
		comment = p.parseCommentOnly(ctx)
		if comment != nil {
			comment.SetPath(ctx.withChild(key.GetToken().Value).path)
		}
		tk = p.currentToken()
	}
	if tk.Position.Column == key.GetToken().Position.Column && tk.Type == token.StringType {
		// in this case,
		// ----
		// key: <value does not defined>
		// next

		nullToken := p.createNullToken(colonToken)
		p.insertToken(p.idx, nullToken)
		nullNode := ast.Null(nullToken)

		if comment != nil {
			_ = nullNode.SetComment(comment)
		} else {
			// If there is a comment, it is already bound to the key node,
			// so remove the comment from the key to bind it to the null value.
			keyComment := key.GetComment()
			if keyComment != nil {
				if err := key.SetComment(nil); err != nil {
					return nil, err
				}
				_ = nullNode.SetComment(keyComment)
			}
		}
		return nullNode, nil
	}

	if tk.Position.Column < key.GetToken().Position.Column {
		// in this case,
		// ----
		//   key: <value does not defined>
		// next
		nullToken := p.createNullToken(colonToken)
		p.insertToken(p.idx, nullToken)
		nullNode := ast.Null(nullToken)
		if comment != nil {
			_ = nullNode.SetComment(comment)
		}
		return nullNode, nil
	}

	value, err := p.parseToken(ctx, p.currentToken())
	if err != nil {
		return nil, err
	}
	if comment != nil {
		_ = value.SetComment(comment)
	}
	return value, nil
}

func (p *parser) validateMapValue(ctx *context, key, value ast.Node) error {
	keyTk := key.GetToken()
	valueTk := value.GetToken()

	if keyTk.Position.Line == valueTk.Position.Line && valueTk.Type == token.SequenceEntryType {
		return errors.ErrSyntax("block sequence entries are not allowed in this context", valueTk)
	}
	if keyTk.Position.Column != valueTk.Position.Column {
		return nil
	}
	if value.Type() != ast.StringType {
		return nil
	}
	ntk := p.nextToken()
	if ntk == nil || (ntk.Type != token.MappingValueType && ntk.Type != token.SequenceEntryType) {
		return errors.ErrSyntax("could not find expected ':' token", valueTk)
	}
	return nil
}

func (p *parser) parseMappingValue(ctx *context) (ast.Node, error) {
	key, err := p.parseMapKey(ctx)
	if err != nil {
		return nil, err
	}
	keyText := key.GetToken().Value
	key.SetPath(ctx.withChild(keyText).path)
	if err := p.validateMapKey(key.GetToken()); err != nil {
		return nil, err
	}
	p.progress(1)          // progress to mapping value token
	tk := p.currentToken() // get mapping value token
	if tk == nil {
		return nil, errors.ErrSyntax("unexpected map", key.GetToken())
	}
	p.progress(1) // progress to value token
	if err := p.setSameLineCommentIfExists(ctx.withChild(keyText), key); err != nil {
		return nil, err
	}
	if key.GetComment() != nil {
		// if current token is comment, GetComment() is not nil.
		// then progress to value token
		p.progressIgnoreComment(1)
	}

	value, err := p.parseMapValue(ctx.withChild(keyText), key, tk)
	if err != nil {
		return nil, err
	}
	if err := p.validateMapValue(ctx, key, value); err != nil {
		return nil, err
	}

	mvnode := ast.MappingValue(tk, key, value)
	mvnode.SetPath(ctx.withChild(keyText).path)
	node := ast.Mapping(tk, false, mvnode)
	node.SetPath(ctx.withChild(keyText).path)

	ntk := p.nextNotCommentToken()
	antk := p.afterNextNotCommentToken()
	for ntk != nil && ntk.Position.Column == key.GetToken().Position.Column {
		if ntk.Type == token.DocumentHeaderType || ntk.Type == token.DocumentEndType {
			break
		}
		if antk == nil {
			return nil, errors.ErrSyntax("required ':' and map value", ntk)
		}
		if antk.Type != token.MappingValueType {
			return nil, errors.ErrSyntax("required ':' and map value", antk)
		}
		p.progressIgnoreComment(1)
		value, err := p.parseToken(ctx, p.currentToken())
		if err != nil {
			return nil, err
		}
		switch value.Type() {
		case ast.MappingType:
			c, _ := value.(*ast.MappingNode)
			comment := c.GetComment()
			for idx, v := range c.Values {
				if idx == 0 && comment != nil {
					if err := v.SetComment(comment); err != nil {
						return nil, err
					}
				}
				node.Values = append(node.Values, v)
			}
		case ast.MappingValueType:
			node.Values = append(node.Values, value.(*ast.MappingValueNode))
		default:
			return nil, fmt.Errorf("failed to parse mapping value node node is %s", value.Type())
		}
		ntk = p.nextNotCommentToken()
		antk = p.afterNextNotCommentToken()
	}
	if len(node.Values) == 1 {
		mapKeyCol := mvnode.Key.GetToken().Position.Column
		commentTk := p.nextToken()
		if commentTk != nil && commentTk.Type == token.CommentType && mapKeyCol <= commentTk.Position.Column {
			// If the comment is in the same or deeper column as the last element column in map value,
			// treat it as a footer comment for the last element.
			comment := p.parseFootComment(ctx, mapKeyCol)
			mvnode.FootComment = comment
		}
		return mvnode, nil
	}
	mapCol := node.GetToken().Position.Column
	commentTk := p.nextToken()
	if commentTk != nil && commentTk.Type == token.CommentType && mapCol <= commentTk.Position.Column {
		// If the comment is in the same or deeper column as the last element column in map value,
		// treat it as a footer comment for the last element.
		comment := p.parseFootComment(ctx, mapCol)
		node.FootComment = comment
	}
	return node, nil
}

func (p *parser) parseSequenceEntry(ctx *context) (*ast.SequenceNode, error) {
	tk := p.currentToken()
	sequenceNode := ast.Sequence(tk, false)
	sequenceNode.SetPath(ctx.path)
	curColumn := tk.Position.Column
	for tk.Type == token.SequenceEntryType {
		p.progress(1) // skip sequence token
		tk = p.currentToken()
		if tk == nil {
			return nil, errors.ErrSyntax("empty sequence value", p.previousToken())
		}
		var comment *ast.CommentGroupNode
		if tk.Type == token.CommentType {
			comment = p.parseCommentOnly(ctx)
			tk = p.currentToken()
			if tk.Type == token.SequenceEntryType {
				p.progress(1) // skip sequence token
			}
		}
		value, err := p.parseToken(ctx.withIndex(uint(len(sequenceNode.Values))), p.currentToken())
		if err != nil {
			return nil, err
		}
		if comment != nil {
			comment.SetPath(ctx.withIndex(uint(len(sequenceNode.Values))).path)
			sequenceNode.ValueHeadComments = append(sequenceNode.ValueHeadComments, comment)
		} else {
			sequenceNode.ValueHeadComments = append(sequenceNode.ValueHeadComments, nil)
		}
		sequenceNode.Values = append(sequenceNode.Values, value)
		tk = p.nextNotCommentToken()
		if tk == nil {
			break
		}
		if tk.Type != token.SequenceEntryType {
			break
		}
		if tk.Position.Column != curColumn {
			break
		}
		p.progressIgnoreComment(1)
	}
	commentTk := p.nextToken()
	if commentTk != nil && commentTk.Type == token.CommentType && curColumn <= commentTk.Position.Column {
		// If the comment is in the same or deeper column as the last element column in sequence value,
		// treat it as a footer comment for the last element.
		comment := p.parseFootComment(ctx, curColumn)
		sequenceNode.FootComment = comment
	}
	return sequenceNode, nil
}

func (p *parser) parseAnchor(ctx *context) (*ast.AnchorNode, error) {
	tk := p.currentToken()
	anchor := ast.Anchor(tk)
	anchor.SetPath(ctx.path)
	ntk := p.nextToken()
	if ntk == nil {
		return nil, errors.ErrSyntax("unexpected anchor. anchor name is undefined", tk)
	}
	p.progress(1) // skip anchor token
	name, err := p.parseToken(ctx, p.currentToken())
	if err != nil {
		return nil, err
	}
	anchor.Name = name
	ntk = p.nextToken()
	if ntk == nil {
		return nil, errors.ErrSyntax("unexpected anchor. anchor value is undefined", p.currentToken())
	}
	p.progress(1)
	value, err := p.parseToken(ctx, p.currentToken())
	if err != nil {
		return nil, err
	}
	anchor.Value = value
	return anchor, nil
}

func (p *parser) parseAlias(ctx *context) (*ast.AliasNode, error) {
	tk := p.currentToken()
	alias := ast.Alias(tk)
	alias.SetPath(ctx.path)
	ntk := p.nextToken()
	if ntk == nil {
		return nil, errors.ErrSyntax("unexpected alias. alias name is undefined", tk)
	}
	p.progress(1) // skip alias token
	name, err := p.parseToken(ctx, p.currentToken())
	if err != nil {
		return nil, err
	}
	alias.Value = name
	return alias, nil
}

func (p *parser) parseMapKey(ctx *context) (ast.MapKeyNode, error) {
	tk := p.currentToken()
	if value := p.parseScalarValue(tk); value != nil {
		return value, nil
	}
	switch tk.Type {
	case token.MergeKeyType:
		return ast.MergeKey(tk), nil
	case token.MappingKeyType:
		return p.parseMappingKey(ctx)
	}
	return nil, errors.ErrSyntax("unexpected mapping key", tk)
}

func (p *parser) parseStringValue(tk *token.Token) *ast.StringNode {
	switch tk.Type {
	case token.StringType,
		token.SingleQuoteType,
		token.DoubleQuoteType:
		return ast.String(tk)
	}
	return nil
}

func (p *parser) parseScalarValueWithComment(ctx *context, tk *token.Token) (ast.ScalarNode, error) {
	node := p.parseScalarValue(tk)
	if node == nil {
		return nil, nil
	}
	node.SetPath(ctx.path)
	if p.isSameLineComment(p.nextToken(), node) {
		p.progress(1)
		if err := p.setSameLineCommentIfExists(ctx, node); err != nil {
			return nil, err
		}
	}
	return node, nil
}

func (p *parser) parseScalarValue(tk *token.Token) ast.ScalarNode {
	if node := p.parseStringValue(tk); node != nil {
		return node
	}
	switch tk.Type {
	case token.NullType:
		return ast.Null(tk)
	case token.BoolType:
		return ast.Bool(tk)
	case token.IntegerType,
		token.BinaryIntegerType,
		token.OctetIntegerType,
		token.HexIntegerType:
		return ast.Integer(tk)
	case token.FloatType:
		return ast.Float(tk)
	case token.InfinityType:
		return ast.Infinity(tk)
	case token.NanType:
		return ast.Nan(tk)
	}
	return nil
}

func (p *parser) parseDirective(ctx *context) (*ast.DirectiveNode, error) {
	node := ast.Directive(p.currentToken())
	p.progress(1) // skip directive token
	value, err := p.parseToken(ctx, p.currentToken())
	if err != nil {
		return nil, err
	}
	node.Value = value
	p.progress(1)
	tk := p.currentToken()
	if tk == nil {
		// Since current token is nil, use the previous token to specify
		// the syntax error location.
		return nil, errors.ErrSyntax("unexpected directive value. document not started", p.previousToken())
	}
	if tk.Type != token.DocumentHeaderType {
		return nil, errors.ErrSyntax("unexpected directive value. document not started", p.currentToken())
	}
	return node, nil
}

func (p *parser) parseLiteral(ctx *context) (*ast.LiteralNode, error) {
	node := ast.Literal(p.currentToken())
	p.progress(1) // skip literal/folded token

	tk := p.currentToken()
	if tk == nil {
		node.Value = ast.String(token.New("", "", node.Start.Position))
		return node, nil
	}
	var comment *ast.CommentGroupNode
	if tk.Type == token.CommentType {
		comment = p.parseCommentOnly(ctx)
		comment.SetPath(ctx.path)
		if err := node.SetComment(comment); err != nil {
			return nil, err
		}
		tk = p.currentToken()
	}
	value, err := p.parseToken(ctx, tk)
	if err != nil {
		return nil, err
	}
	snode, ok := value.(*ast.StringNode)
	if !ok {
		return nil, errors.ErrSyntax("unexpected token. required string token", value.GetToken())
	}
	node.Value = snode
	return node, nil
}

func (p *parser) isSameLineComment(tk *token.Token, node ast.Node) bool {
	if tk == nil {
		return false
	}
	if tk.Type != token.CommentType {
		return false
	}
	return tk.Position.Line == node.GetToken().Position.Line
}

func (p *parser) setSameLineCommentIfExists(ctx *context, node ast.Node) error {
	tk := p.currentToken()
	if !p.isSameLineComment(tk, node) {
		return nil
	}
	comment := ast.CommentGroup([]*token.Token{tk})
	comment.SetPath(ctx.path)
	if err := node.SetComment(comment); err != nil {
		return err
	}
	return nil
}

func (p *parser) parseDocument(ctx *context) (*ast.DocumentNode, error) {
	startTk := p.currentToken()
	p.progress(1) // skip document header token
	body, err := p.parseToken(ctx, p.currentToken())
	if err != nil {
		return nil, err
	}
	node := ast.Document(startTk, body)
	if ntk := p.nextToken(); ntk != nil && ntk.Type == token.DocumentEndType {
		node.End = ntk
		p.progress(1)
	}
	return node, nil
}

func (p *parser) parseCommentOnly(ctx *context) *ast.CommentGroupNode {
	commentTokens := []*token.Token{}
	for {
		tk := p.currentToken()
		if tk == nil {
			break
		}
		if tk.Type != token.CommentType {
			break
		}
		commentTokens = append(commentTokens, tk)
		p.progressIgnoreComment(1) // skip comment token
	}
	return ast.CommentGroup(commentTokens)
}

func (p *parser) parseFootComment(ctx *context, col int) *ast.CommentGroupNode {
	commentTokens := []*token.Token{}
	for {
		p.progressIgnoreComment(1)
		commentTokens = append(commentTokens, p.currentToken())

		nextTk := p.nextToken()
		if nextTk == nil {
			break
		}
		if nextTk.Type != token.CommentType {
			break
		}
		if col > nextTk.Position.Column {
			break
		}
	}
	return ast.CommentGroup(commentTokens)
}

func (p *parser) parseComment(ctx *context) (ast.Node, error) {
	group := p.parseCommentOnly(ctx)
	node, err := p.parseToken(ctx, p.currentToken())
	if err != nil {
		return nil, err
	}
	if node == nil {
		return group, nil
	}
	group.SetPath(node.GetPath())
	if err := node.SetComment(group); err != nil {
		return nil, err
	}
	return node, nil
}

func (p *parser) parseMappingKey(ctx *context) (*ast.MappingKeyNode, error) {
	keyTk := p.currentToken()
	node := ast.MappingKey(keyTk)
	node.SetPath(ctx.path)
	p.progress(1) // skip mapping key token
	value, err := p.parseToken(ctx.withChild(keyTk.Value), p.currentToken())
	if err != nil {
		return nil, err
	}
	node.Value = value
	return node, nil
}

func (p *parser) parseToken(ctx *context, tk *token.Token) (ast.Node, error) {
	node, err := p.createNodeFromToken(ctx, tk)
	if err != nil {
		return nil, err
	}
	if node != nil && node.GetPath() == "" {
		node.SetPath(ctx.path)
	}
	return node, nil
}

func (p *parser) createNodeFromToken(ctx *context, tk *token.Token) (ast.Node, error) {
	if tk == nil {
		return nil, nil
	}
	if tk.NextType() == token.MappingValueType {
		node, err := p.parseMappingValue(ctx)
		return node, err
	}
	node, err := p.parseScalarValueWithComment(ctx, tk)
	if err != nil {
		return nil, err
	}
	if node != nil {
		return node, nil
	}
	switch tk.Type {
	case token.CommentType:
		return p.parseComment(ctx)
	case token.MappingKeyType:
		return p.parseMappingKey(ctx)
	case token.DocumentHeaderType:
		return p.parseDocument(ctx)
	case token.MappingStartType:
		return p.parseMapping(ctx)
	case token.SequenceStartType:
		return p.parseSequence(ctx)
	case token.SequenceEndType:
		// SequenceEndType is always validated in parseSequence.
		// Therefore, if this is found in other cases, it is treated as a syntax error.
		return nil, errors.ErrSyntax("could not find '[' character corresponding to ']'", tk)
	case token.SequenceEntryType:
		return p.parseSequenceEntry(ctx)
	case token.AnchorType:
		return p.parseAnchor(ctx)
	case token.AliasType:
		return p.parseAlias(ctx)
	case token.DirectiveType:
		return p.parseDirective(ctx)
	case token.TagType:
		return p.parseTag(ctx)
	case token.LiteralType, token.FoldedType:
		return p.parseLiteral(ctx)
	}
	return nil, nil
}

func (p *parser) parse(ctx *context) (*ast.File, error) {
	file := &ast.File{Docs: []*ast.DocumentNode{}}
	for p.next() {
		node, err := p.parseToken(ctx, p.currentToken())
		if err != nil {
			return nil, err
		}
		p.progressIgnoreComment(1)
		if node == nil {
			continue
		}
		if doc, ok := node.(*ast.DocumentNode); ok {
			file.Docs = append(file.Docs, doc)
		} else {
			file.Docs = append(file.Docs, ast.Document(nil, node))
		}
	}
	return file, nil
}

type Mode uint

const (
	ParseComments Mode = 1 << iota // parse comments and add them to AST
)

// ParseBytes parse from byte slice, and returns ast.File
func ParseBytes(bytes []byte, mode Mode) (*ast.File, error) {
	tokens := lexer.Tokenize(string(bytes))
	f, err := Parse(tokens, mode)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Parse parse from token instances, and returns ast.File
func Parse(tokens token.Tokens, mode Mode) (*ast.File, error) {
	if tk := tokens.InvalidToken(); tk != nil {
		return nil, errors.ErrSyntax("found invalid token", tk)
	}
	f, err := newParser(tokens, mode).parse(newContext())
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Parse parse from filename, and returns ast.File
func ParseFile(filename string, mode Mode) (*ast.File, error) {
	file, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	f, err := ParseBytes(file, mode)
	if err != nil {
		return nil, err
	}
	f.Name = filename
	return f, nil
}
