;; TypeScript Tree-sitter queries for CodeGuard

;; Extends JavaScript queries with TS-specific nodes

;; Function declarations
(function_declaration
  name: (identifier) @function.name
  parameters: (formal_parameters) @function.params
  body: (statement_block) @function.body)

;; Arrow functions
(arrow_function
  parameters: (formal_parameters) @function.params
  body: (_) @function.body)

;; Class declarations
(class_declaration
  name: (type_identifier) @class.name
  super_class: (identifier)? @class.extends
  body: (class_body) @class.body)

;; Interface declarations
(interface_declaration
  name: (type_identifier) @interface.name
  (interface_heritage)? @interface.extends
  body: (object_type) @interface.body)

;; Method definitions
(method_definition
  name: (property_identifier) @method.name
  parameters: (formal_parameters) @method.params
  body: (statement_block) @method.body)

;; Import statements
(import_statement
  source: (string) @import.source)

;; Call expressions
(call_expression
  function: (_) @call.function
  arguments: (arguments) @call.args)
