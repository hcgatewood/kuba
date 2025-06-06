help: ## Print this help text
	@grep -Eh '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

build: ## Build the package
	rm -rf dist
	poetry build

install: build ## Dev install the package locally
	pip install --force-reinstall dist/*.whl

uninstall: ## Dev uninstall the package locally
	pip uninstall -y $(shell poetry version | cut -d ' ' -f 1)

publish: ## Publish the package to PyPI
	rm -rf dist
	poetry publish --build

demo: ## Create the demo
	vhs assets/demo.tape --output assets/demo.gif

demo_open: demo ## Create the demo then open it
	open assets
