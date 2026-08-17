;; Python Tree-sitter queries for CodeGuard

;; Function definitions
(function_definition
  name: (identifier) @function.name
  parameters: (parameters) @function.params
  body: (block) @function.body)

;; Class definitions
(class_definition
  name: (identifier) @class.name
  superclasses: (argument_list)? @class.bases
  body: (block) @class.body)

;; Method definitions inside classes
(function_definition
  name: (identifier) @method.name
  parameters: (parameters
    (identifier) @method.self)
  body: (block) @method.body)

;; Import statements
(import_statement
  (dotted_name) @import.name)

(import_from_statement
  module_name: (dotted_name) @import.module
  (dotted_name) @import.name)

;; Call expressions
(call
  function: (_) @call.function
  arguments: (argument_list) @call.args)
